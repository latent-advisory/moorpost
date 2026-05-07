// Enumerates Claude session JSONLs in ~/.claude/projects/<encoded-cwd>/
// for the handoff/return picker UIs.
//
// Each session is a single JSONL file named <uuid>.jsonl. We extract:
//   - sessionId (the filename UUID)
//   - mtime (most-recent-write — used to sort)
//   - firstUserText (first `type:"user"` line whose content has a text part)
//   - sizeBytes (rough proxy for activity)
//
// Encoding: claude derives the project key by replacing every char in
// the absolute cwd path that isn't [a-zA-Z0-9-] with `-`. We mirror the
// same transform here so the path matches what claude already created.
//
// Performance: we read at most the first ~200 lines per JSONL to find
// the first user message. Real sessions get the user text in lines 1-5;
// the cap protects against huge JSONLs (we have one >18MB) where a
// full read would hang the picker.

import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import * as readline from 'readline';

export interface SessionInfo {
  sessionId: string;
  mtimeMs: number;
  sizeBytes: number;
  firstUserText: string;
  jsonlPath: string;
}

const MAX_SCAN_LINES = 200;
const MAX_LABEL_CHARS = 80;

export function encodeCwd(absCwd: string): string {
  return absCwd.replace(/[^a-zA-Z0-9-]/g, '-');
}

export function sessionsDir(absCwd: string): string {
  return path.join(os.homedir(), '.claude', 'projects', encodeCwd(absCwd));
}

/**
 * Return all project directories under ~/.claude/projects/ that correspond
 * to the given workspace root or any of its subdirectories.
 *
 * Claude stores each session under a key derived from the cwd at the time
 * the session was started. A user may run `claude` from a subdirectory of
 * the workspace (e.g. code/ifrs3_extractor) — those sessions land in a
 * different projects/ folder and would be invisible to the picker if we
 * only scanned the exact workspace-root directory. We scan all directories
 * whose encoded name equals the encoded workspace root OR starts with
 * "<encoded-root>-" (the `-` comes from the `/` separator being replaced).
 */
async function allProjectDirsForWorkspace(absCwd: string): Promise<string[]> {
  const base = path.join(os.homedir(), '.claude', 'projects');
  const encoded = encodeCwd(absCwd);
  let allDirs: string[];
  try {
    allDirs = await fs.promises.readdir(base);
  } catch {
    return [];
  }
  return allDirs
    .filter((d) => d === encoded || d.startsWith(encoded + '-'))
    .map((d) => path.join(base, d));
}

export async function listLocalSessions(absCwd: string): Promise<SessionInfo[]> {
  const dirs = await allProjectDirsForWorkspace(absCwd);
  const seen = new Set<string>();
  const sessions: SessionInfo[] = [];
  for (const dir of dirs) {
    let entries: string[];
    try {
      entries = await fs.promises.readdir(dir);
    } catch {
      continue;
    }
    for (const entry of entries) {
      if (!entry.endsWith('.jsonl')) continue;
      const sessionId = entry.slice(0, -'.jsonl'.length);
      if (!isUuid(sessionId)) continue;
      if (seen.has(sessionId)) continue;
      seen.add(sessionId);
      const jsonlPath = path.join(dir, entry);
      let stat: fs.Stats;
      try {
        stat = await fs.promises.stat(jsonlPath);
      } catch {
        continue;
      }
      const firstUserText = await extractFirstUserText(jsonlPath);
      sessions.push({
        sessionId,
        mtimeMs: stat.mtimeMs,
        sizeBytes: stat.size,
        firstUserText,
        jsonlPath,
      });
    }
  }
  sessions.sort((a, b) => b.mtimeMs - a.mtimeMs);
  return sessions;
}

function isUuid(s: string): boolean {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s);
}

async function extractFirstUserText(jsonlPath: string): Promise<string> {
  let stream: fs.ReadStream;
  try {
    stream = fs.createReadStream(jsonlPath, { encoding: 'utf8' });
  } catch {
    return '(no preview)';
  }
  const rl = readline.createInterface({ input: stream, crlfDelay: Infinity });
  let scanned = 0;
  let result = '';
  try {
    for await (const line of rl) {
      scanned++;
      if (scanned > MAX_SCAN_LINES) break;
      const trimmed = line.trim();
      if (!trimmed) continue;
      let event: unknown;
      try {
        event = JSON.parse(trimmed);
      } catch {
        continue;
      }
      const text = pluckUserText(event);
      if (text && !isSyntheticUserMessage(text)) {
        result = text;
        break;
      }
    }
  } finally {
    rl.close();
    stream.close();
  }
  if (!result) return '(no user message yet)';
  const trimmed = result.replace(/\s+/g, ' ').trim();
  return trimmed.length > MAX_LABEL_CHARS
    ? trimmed.slice(0, MAX_LABEL_CHARS - 1) + '…'
    : trimmed;
}

interface UserEventShape {
  type?: string;
  message?: {
    content?: unknown;
  };
}

/**
 * Claude Code writes synthetic `type:"user"` events into the JSONL
 * for IDE-context dumps, slash-command metadata, system reminders,
 * and bash output. They aren't actual prompts the human typed, so
 * skip them when picking the session label — show the first REAL
 * prompt instead. Identified by a known XML-ish opening tag at the
 * start of the message text.
 */
function isSyntheticUserMessage(text: string): boolean {
  const t = text.trimStart();
  if (!t.startsWith('<')) return false;
  return SYNTHETIC_USER_TAGS.some((tag) => t.startsWith(tag));
}

const SYNTHETIC_USER_TAGS = [
  '<ide_opened_file>',
  '<ide_selection>',
  '<local-command-caveat>',
  '<local-command-stdout>',
  '<local-command-stderr>',
  '<local-command-output>',
  '<command-name>',
  '<command-message>',
  '<command-args>',
  '<system-reminder>',
  '<user-prompt-submit-hook>',
  '<bash-stdout>',
  '<bash-stderr>',
];

function pluckUserText(event: unknown): string | undefined {
  if (!event || typeof event !== 'object') return undefined;
  const e = event as UserEventShape;
  if (e.type !== 'user') return undefined;
  const content = e.message?.content;
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    for (const part of content) {
      if (
        part &&
        typeof part === 'object' &&
        (part as { type?: string }).type === 'text'
      ) {
        const text = (part as { text?: unknown }).text;
        if (typeof text === 'string' && text) return text;
      }
    }
  }
  return undefined;
}
