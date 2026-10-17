# Documentation

The index for everything under `docs/`. AGENTS.md points here rather than
enumerating these files, so docs can move without leaving the agent guide stale.

## Read for...

| Question | Read |
|---|---|
| What Bloud is, and which apps ship | [../README.md](../README.md) (the catalog list and login table are generated from the app metadata) |
| Runtime and release engineering policy | [specs/spec.md](specs/spec.md) |
| Orchestrator/reconciler design | [specs/reconciler-spec.md](specs/reconciler-spec.md) |
| Multi-container app model | [specs/app-spec.md](specs/app-spec.md) |
| Component overview + data flows | [architecture/overview.md](architecture/overview.md) |
| The full app + container graph (generated from the catalog) | [../README.md#the-full-graph](../README.md#the-full-graph) |
| How to add an app | [guides/contributing-apps.md](guides/contributing-apps.md) |
| Run Bloud locally / hot-reload the control plane and dashboard | [../services/host-agent/README.md](../services/host-agent/README.md#development) |
| Run Vaultwarden in dev (needs `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1` over plain HTTP) | [../apps/vaultwarden/INTEGRATION.md](../apps/vaultwarden/INTEGRATION.md#plain-http) |
| Backend debt + repayment plan | [operations/tech-debt.md](operations/tech-debt.md), the per-slice plan [plans/tech-debt-repayment.md](plans/tech-debt-repayment.md), and the closed-item record [operations/archive/tech-debt-history.md](operations/archive/tech-debt-history.md) |
| Build the .deb release package | [operations/packaging.md](operations/packaging.md) |
| Back up, restore, and update an install | [operations/backup-restore.md](operations/backup-restore.md) |
| How MCP servers work in Bloud (shipped: the catalog carries MCP providers) | [features/mcp.md](features/mcp.md) |
| Dashboard layout + widgets | [features/dashboard.md](features/dashboard.md) |
| Agent API + multi-port apps (`extraPorts`, the `agentApi` contract) | [features/agent-api.md](features/agent-api.md) |
| Speech-to-text: the instance's transcription endpoint and the `transcription` contract | [features/transcription.md](features/transcription.md) |
| The instance clock: the time zone every container gets | [features/timezone.md](features/timezone.md) |
| How Bloud ships app credentials to clients (declare, reveal, rotate, revoke) | [features/client-credentials.md](features/client-credentials.md) (shipped); the design record is [plans/archive/client-credentials.md](plans/archive/client-credentials.md) |
| Every dated review of this codebase, oldest first | [reviews/](reviews/) |
| The review that found the remote admin bypass, and the first full audit | [reviews/2026-09-17.md](reviews/2026-09-17.md), [reviews/2026-09-19.md](reviews/2026-09-19.md) |
| Design and complexity reviews, APoSD lens | [reviews/2026-10-03-aposd.md](reviews/2026-10-03-aposd.md) (module shapes), [reviews/2026-10-12-aposd-appsurface.md](reviews/2026-10-12-aposd-appsurface.md) (the app-extension surface) |
| In-flight designs | [plans/](plans/) |
| CI flakiness: measured root causes and the fix order | [plans/ci-flakiness-reduction.md](plans/ci-flakiness-reduction.md) |
| How catalog changes apply to running installs | [plans/catalog-update-reconciliation.md](plans/catalog-update-reconciliation.md) |
| How Bloud ships and updates via apt | [plans/apt-repository.md](plans/apt-repository.md) |
| Making installs show what they will do | [plans/install-experience.md](plans/install-experience.md) |
| One frontend store, its state machines, and hot reload | [plans/frontend-fsm.md](plans/frontend-fsm.md) |
| HTTPS with publicly trusted certificates | [plans/tls.md](plans/tls.md) |
| Giving the instance a domain and a certificate, and why not Nginx Proxy Manager | [plans/domain-automation.md](plans/domain-automation.md) |
| Putting the media library on a NAS: storage roots, and how an unprivileged host agent mounts one | [plans/storage-roots.md](plans/storage-roots.md) |
| One inference endpoint for the whole instance, filled in under Settings | [plans/inference-provider-settings.md](plans/inference-provider-settings.md) |
| Tools an agent asks Bloud itself, and where wrappers stop | [plans/native-mcp-endpoint.md](plans/native-mcp-endpoint.md) |
| A personal calendar for each user, and what the agent then needs | [plans/per-user-calendars.md](plans/per-user-calendars.md) |
| One CalDAV endpoint that aggregates the calendars the apps publish | [plans/radicale-calendar-aggregation.md](plans/radicale-calendar-aggregation.md) |
| Finished plans, kept for the reasoning | [plans/archive/](plans/archive/) |
| Why adding an app touches 53% of its files outside `apps/`, and the six derivations that fix it | [plans/archive/app-add-tax.md](plans/archive/app-add-tax.md) (landed) |
| A smaller, clearer app-SDK surface: group `Deps`, one `Secret` type, a binder table, then extract the SDK | [plans/archive/configurator-sdk-surface.md](plans/archive/configurator-sdk-surface.md) (landed) |
| Handing app credentials to third-party clients (reveal vs provision) | [plans/archive/client-credentials.md](plans/archive/client-credentials.md) (shipped) |
| Pilot: hermes-webui, OIDC for browsers plus a minted password for Hermex | [plans/archive/client-credentials-hermes-webui-pilot.md](plans/archive/client-credentials-hermes-webui-pilot.md) (shipped) |
| One MCP provider over Seerr and the arr stack, with many optional integrations | [plans/archive/arr-mcp-provider.md](plans/archive/arr-mcp-provider.md) (shipped) |
| A standalone MCP container for Home Assistant, and the token it has to mint | [plans/archive/home-assistant-mcp.md](plans/archive/home-assistant-mcp.md) (shipped) |
| MCP as an ordinary catalog capability: the decision history, including the reversal | [plans/archive/mcp-integrations.md](plans/archive/mcp-integrations.md) (shipped; phases 2 and 4 open) |
| External apps: launchers, remote installs, and off-host providers | [plans/archive/external-apps.md](plans/archive/external-apps.md) (shipped) |
| Wiring the media stack: Sonarr, Radarr, qBittorrent, Prowlarr, and Seerr | [plans/archive/media-stack-integration.md](plans/archive/media-stack-integration.md) (app side landed; framework gaps F2-F6 open) |
| Hermes for native clients, and the credential the Hermex path needs | [plans/archive/hermes-gateway.md](plans/archive/hermes-gateway.md) (goal 1 shipped; the Hermex decision is open) |

## Sections

- **specs/**: authoritative references: the release plan, the reconciler spec,
  and the app spec.
- **reviews/**: every dated review of this codebase, oldest first. A review is a
  snapshot of what was true on its date; each one carries a note of what has
  since been repaid. New findings never go in a review: they go in the ledger
  under `operations/`.
- **architecture/**: system design overview and data flows.
- **guides/**: how-to documentation.
- **features/**: per-feature documentation.
- **operations/**: maintenance and devOps, including the backend debt ledger. Finished
  debt narratives live in `operations/archive/`.
- **plans/**: design plans. Every `plans/*.md` starts with
  `> Status: draft | accepted | landed | retired | dropped`; finished plans move
  to `plans/archive/`, with their relative links rewritten.
