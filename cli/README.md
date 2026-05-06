# moorpost CLI

The `moorpost` command-line tool — the engine of Moorpost. The VSCode extension is a UI on top of it; terminal-only users use this directly.

## Layout

```
cli/
├── main.go            # entrypoint
├── cmd/               # cobra commands (root + subcommands)
└── internal/
    ├── provider/      # cloud-provider abstraction (gcp, ...)
    ├── agent/         # AI-coding-agent abstraction (claude-code, ...)
    ├── sync/          # file-sync engine abstraction (mutagen, rsync, ...)
    ├── session/       # bundles provider+agent+sync per project
    ├── ssh/           # ~/.ssh/config writer, ControlMaster (later)
    ├── tmux/          # tmux orchestration over SSH (later)
    ├── keychain/      # macOS security / Linux secret-tool (later)
    ├── state/         # ~/.moorpost/state.json read/write (later)
    └── config/        # .moorpost/config.yaml schema (later)
```

The three interface packages — `provider`, `agent`, `sync` — are the heart of the design. CLI commands consume them only by interface, never by concrete type. New cloud providers or AI agents drop in via `Register("name", New)` calls in their package's `init()`.

## Build

```
cd cli
go build ./...
go test ./...
./moorpost --version
```

## Status

Pre-alpha — the v0.1 walking skeleton is being built iteratively. See [../PLUGIN.md](../PLUGIN.md) §9 for the milestone list.
