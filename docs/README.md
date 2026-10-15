# Documentation

The index for everything under `docs/`. AGENTS.md points here rather than
enumerating these files, so docs can move without leaving the agent guide stale.

## Read for...

| Question | Read |
|---|---|
| What Bloud is, and which apps ship | [../README.md](../README.md) (the catalog list and login table are generated from the app metadata) |
| Runtime and release engineering policy | [specs/spec.md](specs/spec.md) |
| Orchestrator/reconciler design | [specs/reconciler-spec.md](specs/reconciler-spec.md) |
| Component overview + data flows | [architecture/overview.md](architecture/overview.md) |
| The full app + container graph (generated from the catalog) | [../README.md#the-full-graph](../README.md#the-full-graph) |
| How to add an app | [guides/contributing-apps.md](guides/contributing-apps.md) |
| Run Bloud locally / hot-reload the control plane and dashboard | [../services/host-agent/README.md](../services/host-agent/README.md#development) |
| Run Vaultwarden in dev (needs `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1` over plain HTTP) | [../apps/vaultwarden/INTEGRATION.md](../apps/vaultwarden/INTEGRATION.md#plain-http) |
| Multi-container app model | [specs/app-spec.md](specs/app-spec.md) |
| Backend debt + repayment plan | [operations/tech-debt.md](operations/tech-debt.md), the per-slice plan [plans/tech-debt-repayment.md](plans/tech-debt-repayment.md), and the closed-item record [operations/archive/tech-debt-history.md](operations/archive/tech-debt-history.md) |
| Build the .deb release package | [operations/packaging.md](operations/packaging.md) |
| Back up, restore, and update an install | [operations/backup-restore.md](operations/backup-restore.md) |
| How MCP servers work in Bloud (shipped: the catalog carries MCP providers) | [features/mcp.md](features/mcp.md) |
| Dashboard layout + widgets | [features/dashboard.md](features/dashboard.md) |
| Agent API + multi-port apps (`extraPorts`, the `agentApi` contract) | [features/agent-api.md](features/agent-api.md) |
| Speech-to-text: the instance's transcription endpoint and the `transcription` contract | [features/transcription.md](features/transcription.md) |
| Reviews of the runtime, oldest first | [specs/review-2026-09-17.md](specs/review-2026-09-17.md), [specs/review-2026-09-19.md](specs/review-2026-09-19.md) |
| Design and complexity reviews, APoSD lens | [specs/review-2026-10-03-aposd.md](specs/review-2026-10-03-aposd.md) (module shapes), [specs/review-2026-10-12-aposd-appsurface.md](specs/review-2026-10-12-aposd-appsurface.md) (the app-extension surface) |
| CI flakiness: measured root causes and the fix order | [plans/ci-flakiness-reduction.md](plans/ci-flakiness-reduction.md) |
| How catalog changes apply to running installs | [plans/catalog-update-reconciliation.md](plans/catalog-update-reconciliation.md) |
| How Bloud ships and updates via apt | [plans/apt-repository.md](plans/apt-repository.md) |
| Making installs show what they will do | [plans/install-experience.md](plans/install-experience.md) |
| One frontend store, its state machines, and hot reload | [plans/frontend-fsm.md](plans/frontend-fsm.md) |
| Handing app credentials to third-party clients (reveal vs provision) | [features/client-credentials.md](features/client-credentials.md) (shipped); the design record is [plans/archive/client-credentials.md](plans/archive/client-credentials.md) |
| Pilot: hermes-webui, OIDC for browsers plus a minted password for Hermex | [plans/archive/client-credentials-hermes-webui-pilot.md](plans/archive/client-credentials-hermes-webui-pilot.md) (shipped) |
| Tools an agent asks Bloud itself, and where wrappers stop | [plans/native-mcp-endpoint.md](plans/native-mcp-endpoint.md) |
| One MCP provider over Seerr and the arr stack, with many optional integrations | [plans/arr-mcp-provider.md](plans/arr-mcp-provider.md) |
| A standalone MCP container for Home Assistant, and the token it has to mint | [plans/home-assistant-mcp.md](plans/home-assistant-mcp.md) |
| Why adding an app touches 53% of its files outside `apps/`, and the six derivations that fix it | [plans/app-add-tax.md](plans/app-add-tax.md) |
| A smaller, clearer app-SDK surface: group `Deps`, one `Secret` type, a binder table, then extract the SDK | [plans/configurator-sdk-surface.md](plans/configurator-sdk-surface.md) |
| Putting the media library on a NAS: storage roots, and how an unprivileged host agent mounts one | [plans/storage-roots.md](plans/storage-roots.md) |
| In-flight designs | [plans/](plans/) |
| Finished plans, kept for the reasoning | [plans/archive/](plans/archive/) |

## Sections

- **specs/**: authoritative references: the release plan, the reconciler spec,
  the app spec, and the dated reviews. A review is a snapshot of what was true
  on its date; each one carries a note of what has since been repaid.
- **architecture/**: system design overview and data flows.
- **guides/**: how-to documentation.
- **features/**: per-feature documentation.
- **operations/**: maintenance and devOps, including the backend debt ledger. Finished
  debt narratives live in `operations/archive/`.
- **plans/**: design plans. Every `plans/*.md` starts with
  `> Status: draft | accepted | landed | retired | dropped`; finished plans move
  to `plans/archive/`, with their relative links rewritten.
