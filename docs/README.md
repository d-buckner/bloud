# Documentation

The index for everything under `docs/`. AGENTS.md points here rather than
enumerating these files, so docs can move without leaving the agent guide stale.

## Read for...

| Question | Read |
|---|---|
| What are we building / release plan | [specs/spec.md](specs/spec.md) |
| Orchestrator/reconciler design | [specs/reconciler-spec.md](specs/reconciler-spec.md) |
| Component overview + data flows | [architecture/overview.md](architecture/overview.md) |
| The full app + container graph (generated from the catalog) | [../README.md#the-full-graph](../README.md#the-full-graph) |
| How to add an app | [guides/contributing-apps.md](guides/contributing-apps.md) |
| Run Bloud locally / hot-reload the control plane and dashboard | [../services/host-agent/README.md](../services/host-agent/README.md#development) |
| Run Vaultwarden in dev (needs `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1` over plain HTTP) | [../apps/vaultwarden/INTEGRATION.md](../apps/vaultwarden/INTEGRATION.md#plain-http) |
| Multi-container app model | [specs/app-spec.md](specs/app-spec.md) |
| Backend debt + repayment plan | [operations/tech-debt.md](operations/tech-debt.md) |
| Build the .deb release package | [operations/packaging.md](operations/packaging.md) |
| Back up, restore, and update an install | [operations/backup-restore.md](operations/backup-restore.md) |
| How MCP servers work in Bloud (design, not yet built) | [features/mcp.md](features/mcp.md) |
| Dashboard layout + widgets | [features/dashboard.md](features/dashboard.md) |
| Agent API + multi-port apps (`extraPorts`, the `agentApi` contract) | [features/agent-api.md](features/agent-api.md) |
| Dated review findings | [specs/review-2026-09-17.md](specs/review-2026-09-17.md) |
| Latest architecture/code review (2026-09-19) | [specs/review-2026-09-19.md](specs/review-2026-09-19.md) |
| Design & code review, APoSD lens (2026-10-03) | [specs/review-2026-10-03-aposd.md](specs/review-2026-10-03-aposd.md) |
| CI flakiness: measured root causes and the fix order | [plans/ci-flakiness-reduction.md](plans/ci-flakiness-reduction.md) |
| How catalog changes apply to running installs | [plans/catalog-update-reconciliation.md](plans/catalog-update-reconciliation.md) |
| How Bloud ships and updates via apt | [plans/apt-repository.md](plans/apt-repository.md) |
| Making installs show what they will do | [plans/install-experience.md](plans/install-experience.md) |
| Handing app credentials to third-party clients (reveal vs provision) | [plans/client-credentials.md](plans/client-credentials.md) |
| Pilot: hermes-webui, OIDC for browsers plus a minted password for Hermex | [plans/client-credentials-hermes-webui-pilot.md](plans/client-credentials-hermes-webui-pilot.md) |
| How client credentials work (declare, reveal, rotate, revoke) | [features/client-credentials.md](features/client-credentials.md) |
| Tools an agent asks Bloud itself, and where wrappers stop | [plans/native-mcp-endpoint.md](plans/native-mcp-endpoint.md) |
| One MCP provider over Seerr and the arr stack, with many optional integrations | [plans/arr-mcp-provider.md](plans/arr-mcp-provider.md) |
| In-flight designs | [plans/](plans/) |

## Sections

- **specs/**: authoritative references: the release plan, the reconciler spec,
  the app spec, and the dated review snapshot.
- **architecture/**: system design overview and data flows.
- **guides/**: how-to documentation.
- **features/**: per-feature documentation.
- **operations/**: maintenance and devOps, including the backend debt ledger.
- **plans/**: design plans. Every `plans/*.md` starts with
  `> Status: draft | accepted | landed | retired | dropped`; finished plans move
  to `plans/archive/`.
