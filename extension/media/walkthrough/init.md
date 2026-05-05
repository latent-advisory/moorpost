# Initialize a project

This step writes `.moorpost/config.yaml` into the folder you choose. That file defines:

- **Provider settings** — GCP project, region/zone, machine type
- **Sync settings** — what files to mirror to the VM, with sensible excludes (`.git`, `node_modules`, build artifacts)
- **Mode** — local-first (default) or always-on persistent

**Choosing a folder.** When you run `Initialize`, Moorpost asks which workspace folder to manage. Pick the **repo root** — Moorpost syncs that folder and its contents (minus excludes) to the VM. Sub-folder selection is supported but rarely what you want; sync usually wants the whole project.

**After init.** Inspect `.moorpost/config.yaml` if you want to:

- Tighten sync excludes (e.g., add a `data/` folder you don't want copied)
- Switch machine type (default `e2-standard-2` ≈ $0.067/hr running)
- Pick a different region

**Cost defaults:** ~$0.067/hr while VM runs, ~$4/mo for the disk when stopped. The local-first default keeps the VM stopped between handoffs.
