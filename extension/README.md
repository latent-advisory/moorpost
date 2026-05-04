# Moorpost VSCode extension

Thin UI shell over the [moorpost](../) CLI. Status bar item summarizing project state + commands wrapping the CLI's auth/provision/handoff/return/status flow.

## Status

Pre-alpha (v0.2). Walking-skeleton scope: status bar + 5 commands. Tree view, settings panels, smart handoff prompts, marketplace listing land in subsequent iterations.

## Develop

```bash
cd extension
npm install
npm run build         # esbuild bundles src → dist/extension.js
```

To try it inside VSCode:
1. Open the `extension/` folder in VSCode
2. Press F5 — opens an Extension Development Host with Moorpost loaded
3. Run "Moorpost: Show status" from the command palette

## Package the .vsix

```bash
npm run package       # produces moorpost-X.Y.Z.vsix
```

## Layout

```
extension/
├── package.json       # extension manifest + npm deps
├── tsconfig.json
├── esbuild.js         # bundler config
├── .vscodeignore      # excluded from .vsix
└── src/
    ├── extension.ts   # entrypoint (activate/deactivate)
    ├── cli.ts         # thin wrapper around child_process + moorpost binary
    ├── statusBar.ts   # right-aligned status bar item with periodic refresh
    └── commands/
        └── index.ts   # 5 command handlers wrapping CLI invocations
```

## Architecture

The extension is **intentionally tiny**. Per [PLUGIN.md §6.1](../PLUGIN.md#61-why-extension--cli-not-just-extension), the CLI is the source of truth; the extension is a thin UI shell. When VSCode crashes, sync and tmux keep running. Terminal-only users use the CLI directly without the extension.

Commands shell out to a local `moorpost` binary on PATH (configurable via `moorpost.cliPath` setting). The status bar polls `moorpost status --json` periodically and renders a one-line summary (active side, VM state, MTD cost).

## License

[Apache 2.0](../LICENSE).
