// Top-level flows used by the Bootstrap command and the walkthrough buttons.
// Onboarding has two paths: the native VSCode walkthrough (declared in
// package.json with explanatory media) and the one-shot Bootstrap command
// here. The earlier QuickPick "Get started" wizard was removed in v1.0 —
// it duplicated both paths without adding value.

import * as vscode from 'vscode';
import { listGCloudConfigs, runInTerminal, type GCloudConfig } from '../cli';
import { setBootstrapTerminal } from '../runState';

/**
 * One-shot bootstrap. Asks which folder to initialize (multi-root only),
 * confirms whether to also provision a VM, and runs `moorpost bootstrap`
 * in a terminal — `--yes` is implied because we just confirmed in the UI.
 */
export async function bootstrapProject(): Promise<void> {
  const folders = vscode.workspace.workspaceFolders ?? [];
  if (folders.length === 0) {
    void vscode.window.showWarningMessage(
      'Open a folder in VSCode first — bootstrap initializes the workspace folder you choose.',
    );
    return;
  }

  let target: vscode.WorkspaceFolder | undefined;
  if (folders.length === 1) {
    target = folders[0];
  } else {
    target = await vscode.window.showWorkspaceFolderPick({
      placeHolder: 'Which folder should Moorpost manage?',
    });
    if (!target) return;
  }

  const provisionChoice = await vscode.window.showQuickPick(
    [
      {
        label: 'Skip provisioning (recommended for first run)',
        detail: 'Sets up everything except the VM. Run `moorpost provision` later when you\'re ready.',
        provision: false,
      },
      {
        label: 'Also provision the VM',
        detail: 'Creates a stopped GCP VM (~$4/mo disk fee, ~$0.067/hr while running).',
        provision: true,
      },
    ],
    {
      title: `Bootstrap "${target.name}"`,
      placeHolder: 'Should bootstrap also create the VM at the end?',
    },
  );
  if (!provisionChoice) return;

  // Pre-select the gcloud configuration via a native QuickPick so the user
  // doesn't have to spot the prompt scrolling past in the terminal during
  // `moorpost init`. If the user picks an existing config we hand its name
  // and project to bootstrap as flags, fully bypassing the terminal picker.
  // If they pick "add new" — or we can't reach gcloud at all — we fall back
  // to the in-terminal flow (which still shows a banner).
  const gcloudPick = await pickGCloudConfig();
  if (gcloudPick === undefined) return; // user dismissed

  // Native region picker. Auto-selects based on the user's IANA timezone
  // so EU users don't end up with us-central1 by default. Bootstrap
  // forwards --region to its `init` step.
  const region = await pickRegion();
  if (!region) return;

  // Native machine-type picker so the user sees rates/sizes inline (instead
  // of the in-terminal picker scrolling past during bootstrap). Bootstrap
  // forwards --machine-type to its `init` step.
  const machineType = await pickMachineType();
  if (!machineType) return; // user dismissed

  const args = ['bootstrap', '--yes', `--region=${region}`, `--machine-type=${machineType}`];
  if (provisionChoice.provision) args.push('--provision');
  if (gcloudPick !== 'fallback-to-terminal') {
    args.push(`--gcp-config=${gcloudPick.name}`);
    args.push(`--gcp-project=${gcloudPick.project}`);
  }
  const term = runInTerminal(args, target.uri.fsPath);
  // Tell the status bar that bootstrap is in flight. While this terminal
  // is alive, status-bar clicks focus this terminal instead of routing
  // through toggleSide (which would fire signIn/provision/handoff on top
  // of the still-running bootstrap as its intermediate states tick by).
  setBootstrapTerminal(term);
}

/**
 * Native VSCode picker for the gcloud configuration moorpost should use.
 *
 * Returns:
 *   - a GCloudConfig: user picked an existing config (skip terminal picker)
 *   - "fallback-to-terminal": user wants to add a new account, OR no
 *     configs were found (gcloud not installed / never logged in) — let
 *     `moorpost init` handle the OAuth flow in the terminal
 *   - undefined: user dismissed the picker (caller should abort the action)
 */
async function pickGCloudConfig(): Promise<GCloudConfig | 'fallback-to-terminal' | undefined> {
  const configs = await listGCloudConfigs();
  if (configs.length === 0) {
    // No configs (or gcloud missing). Fall through to the terminal so init's
    // own picker can run `gcloud auth login` and walk the user through.
    return 'fallback-to-terminal';
  }

  // Note: don't reuse the property name `kind` — VSCode's QuickPickItem already
  // defines `kind?: QuickPickItemKind`, and an intersection with our string
  // literals collapses to `undefined`. Use a distinct name.
  type Item = vscode.QuickPickItem & { action: 'config' | 'new'; config?: GCloudConfig };
  const items: Item[] = configs.map<Item>((c) => ({
    label: c.name,
    description: c.is_active ? '(active)' : undefined,
    detail: `account: ${c.account || '(none)'}   project: ${c.project || '(unset)'}`,
    action: 'config',
    config: c,
  }));
  items.push({
    label: '$(add) Add a new gcloud account',
    detail: 'Opens a browser OAuth flow in the terminal — needed only the first time.',
    action: 'new',
  });

  const choice = await vscode.window.showQuickPick(items, {
    title: 'Pick a gcloud configuration for Moorpost',
    placeHolder: 'Moorpost will pin this configuration in .moorpost/config.yaml',
    ignoreFocusOut: true,
  });
  if (!choice) return undefined;
  if (choice.action === 'new' || !choice.config) return 'fallback-to-terminal';

  // A configuration without a project set would force `moorpost init` to
  // re-trigger its own picker (it treats empty project as "ask the user").
  // Surface that here instead of silently falling back, since the user
  // *just* picked this config thinking it was set up.
  if (!choice.config.project) {
    void vscode.window.showWarningMessage(
      `Configuration "${choice.config.name}" has no GCP project set. ` +
        `Run \`gcloud --configuration=${choice.config.name} config set project YOUR_PROJECT\` first, then retry bootstrap.`,
    );
    return undefined;
  }
  return choice.config;
}

/**
 * GCP machine type options offered during init. Rates mirror the gcp
 * package's listPriceTable (us-central1, on-demand list price). Keep
 * these in sync with cli/internal/provider/gcp/gcp.go's listPriceTable.
 *
 * The "light use" monthly estimate assumes ~4h/day of active remote
 * routing × 22 working days = ~88 hours/month. Real usage with the
 * local-first/handoff workflow varies wildly; the number is a rough
 * order-of-magnitude anchor for the picker.
 */
interface MachineTypeOption {
  type: string;
  vCPU: string;
  ramGB: number;
  hourlyUSD: number;
  notes?: string;
}

const MACHINE_TYPE_OPTIONS: MachineTypeOption[] = [
  { type: 'e2-medium', vCPU: '1-2 (shared)', ramGB: 4, hourlyUSD: 0.0335, notes: 'cheapest viable; tight for big builds' },
  { type: 'e2-standard-2', vCPU: '2', ramGB: 8, hourlyUSD: 0.067, notes: 'balanced default' },
  { type: 'e2-standard-4', vCPU: '4', ramGB: 16, hourlyUSD: 0.134, notes: 'heavier builds / monorepos' },
  { type: 'e2-standard-8', vCPU: '8', ramGB: 32, hourlyUSD: 0.268, notes: 'overkill for most solo work' },
];

const RECOMMENDED_MACHINE_TYPE = 'e2-standard-2';
const HOURS_PER_MONTH_LIGHT_USE = 88;

async function pickMachineType(): Promise<string | undefined> {
  interface Item extends vscode.QuickPickItem { type: string }
  const items: Item[] = MACHINE_TYPE_OPTIONS.map((opt) => {
    const monthly = opt.hourlyUSD * HOURS_PER_MONTH_LIGHT_USE;
    const recommended = opt.type === RECOMMENDED_MACHINE_TYPE;
    return {
      label: `${recommended ? '$(star-full) ' : ''}${opt.type}`,
      description: `${opt.vCPU} vCPU · ${opt.ramGB} GB RAM · $${opt.hourlyUSD.toFixed(4)}/hr`,
      detail: `~$${monthly.toFixed(2)}/mo at ${HOURS_PER_MONTH_LIGHT_USE}h light use${opt.notes ? ` — ${opt.notes}` : ''}`,
      type: opt.type,
    };
  });
  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: 'Pick a GCP machine type for the remote VM (★ = recommended)',
    matchOnDescription: true,
    matchOnDetail: true,
  });
  return picked?.type;
}

/**
 * Common GCP regions exposed in the picker. The full GCP catalog is ~40+
 * regions but the auto-install + handoff latency story only really works
 * within a few hundred km of the user, so we expose one canonical pick per
 * major continent and let users edit `.moorpost/config.yaml` for anything
 * exotic. The `tz` array is the IANA-prefix list we use for auto-detect.
 *
 * Hourly rates are e2-standard-2 list price; rate ratios are roughly the
 * same across machine types so this column gives the right "feel" for
 * cost-vs-region trade-offs.
 */
interface RegionOption {
  region: string;
  /** Plain-English location for the picker description. */
  location: string;
  /** e2-standard-2 hourly rate (USD) for "EU is ~10% pricier" intuition. */
  hourlyUSD: number;
  /** IANA timezone prefixes that should auto-select this region. */
  tz: string[];
}

const REGION_OPTIONS: RegionOption[] = [
  { region: 'us-central1', location: 'Iowa, USA', hourlyUSD: 0.067, tz: ['America/Chicago', 'America/Mexico_City', 'America/Bogota', 'America/Lima'] },
  { region: 'us-east1', location: 'South Carolina, USA', hourlyUSD: 0.067, tz: ['America/New_York', 'America/Toronto', 'America/Sao_Paulo', 'America/Argentina/Buenos_Aires'] },
  { region: 'us-west1', location: 'Oregon, USA', hourlyUSD: 0.067, tz: ['America/Los_Angeles', 'America/Vancouver', 'America/Anchorage', 'Pacific/Honolulu'] },
  { region: 'europe-west1', location: 'Belgium', hourlyUSD: 0.0735, tz: ['Europe/London', 'Europe/Dublin', 'Europe/Brussels', 'Europe/Amsterdam', 'Europe/Lisbon', 'Europe/Madrid'] },
  { region: 'europe-west3', location: 'Frankfurt, Germany', hourlyUSD: 0.0773, tz: ['Europe/Berlin', 'Europe/Vienna', 'Europe/Paris', 'Europe/Zurich', 'Europe/Rome', 'Europe/Warsaw', 'Europe/Prague', 'Europe/Copenhagen', 'Europe/Stockholm', 'Europe/Helsinki'] },
  { region: 'asia-northeast1', location: 'Tokyo, Japan', hourlyUSD: 0.0807, tz: ['Asia/Tokyo', 'Asia/Seoul'] },
  { region: 'asia-southeast1', location: 'Singapore', hourlyUSD: 0.0788, tz: ['Asia/Singapore', 'Asia/Bangkok', 'Asia/Hong_Kong', 'Asia/Jakarta', 'Asia/Kuala_Lumpur', 'Asia/Manila', 'Asia/Ho_Chi_Minh', 'Asia/Taipei'] },
  { region: 'australia-southeast1', location: 'Sydney, Australia', hourlyUSD: 0.0958, tz: ['Australia/Sydney', 'Australia/Melbourne', 'Australia/Brisbane', 'Australia/Perth', 'Pacific/Auckland'] },
];

/**
 * Pick the most likely region from the user's IANA timezone. Returns
 * 'us-central1' as a last-resort fallback so the picker always has
 * something pre-selected.
 */
export function inferRegionFromTimezone(tz: string): string {
  for (const opt of REGION_OPTIONS) {
    if (opt.tz.some((prefix) => tz === prefix || tz.startsWith(prefix + '/'))) {
      return opt.region;
    }
  }
  // Continent-level fallbacks for timezones we don't enumerate.
  if (tz.startsWith('Europe/')) return 'europe-west1';
  if (tz.startsWith('Asia/')) return 'asia-southeast1';
  if (tz.startsWith('Australia/') || tz.startsWith('Pacific/')) return 'australia-southeast1';
  if (tz.startsWith('Africa/')) return 'europe-west1'; // Johannesburg etc.: Belgium is the closest GCE region with reasonable latency
  return 'us-central1';
}

async function pickRegion(): Promise<string | undefined> {
  let detected = 'us-central1';
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    detected = inferRegionFromTimezone(tz);
  } catch {
    // Older runtimes may not expose timeZone. Fall back to us-central1.
  }
  interface Item extends vscode.QuickPickItem { region: string }
  const items: Item[] = REGION_OPTIONS.map((opt) => {
    const auto = opt.region === detected;
    return {
      label: `${auto ? '$(star-full) ' : ''}${opt.region}`,
      description: `${opt.location} · $${opt.hourlyUSD.toFixed(4)}/hr (e2-standard-2)`,
      detail: auto ? 'Auto-selected based on your timezone' : undefined,
      region: opt.region,
    };
  });
  // Move the auto-detected option to the top so it's the natural Enter pick.
  items.sort((a, b) => (a.region === detected ? -1 : b.region === detected ? 1 : 0));

  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: 'Pick a GCP region for the VM (★ = closest to you)',
    matchOnDescription: true,
    matchOnDetail: true,
  });
  return picked?.region;
}

/**
 * Folder-aware `moorpost init`. If the workspace has multiple roots, asks
 * which one to initialize. Single-root workspaces skip the picker.
 */
export async function initProject(): Promise<void> {
  const folders = vscode.workspace.workspaceFolders ?? [];
  if (folders.length === 0) {
    void vscode.window.showWarningMessage(
      'Open a folder in VSCode first — Moorpost initializes the workspace folder you choose.',
    );
    return;
  }

  let target: vscode.WorkspaceFolder | undefined;
  if (folders.length === 1) {
    target = folders[0];
  } else {
    target = await vscode.window.showWorkspaceFolderPick({
      placeHolder: 'Which folder should Moorpost manage? (this is what gets synced to the VM)',
    });
    if (!target) return;
  }

  const machineType = await pickMachineType();
  if (!machineType) return;

  const opt = MACHINE_TYPE_OPTIONS.find((o) => o.type === machineType);
  const costLine = opt
    ? `\n\nMachine type: ${opt.type} (${opt.vCPU} vCPU, ${opt.ramGB} GB RAM, $${opt.hourlyUSD.toFixed(4)}/hr)`
    : '';
  const confirm = await vscode.window.showInformationMessage(
    `Initialize Moorpost in "${target.name}"?\n\nThis writes .moorpost/config.yaml. Sync will mirror this folder (minus standard excludes) to the remote VM.${costLine}`,
    { modal: true },
    'Initialize',
  );
  if (confirm !== 'Initialize') return;

  runInTerminal(['init', '--machine-type', machineType], target.uri.fsPath);
}
