# Documentation

The index for everything under `docs/`. AGENTS.md points here rather than
enumerating these files, so docs can move without leaving the agent guide stale.

## Read for...

| Question | Read |
|---|---|
| What are we building / release plan | [specs/spec.md](specs/spec.md) |
| Orchestrator/reconciler design | [specs/reconciler-spec.md](specs/reconciler-spec.md) |
| Component overview + data flows | [architecture/overview.md](architecture/overview.md) |
| The full app + container graph (generated from the catalog) | [architecture/dependency-graph.md](architecture/dependency-graph.md) |
| How to add an app | [guides/contributing-apps.md](guides/contributing-apps.md) |
| Run Vaultwarden in dev (needs `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1` over plain HTTP) | [../apps/vaultwarden/INTEGRATION.md](../apps/vaultwarden/INTEGRATION.md#plain-http) |
| Multi-container app model | [specs/app-spec.md](specs/app-spec.md) |
| Backend debt + repayment plan | [operations/tech-debt.md](operations/tech-debt.md) |
| Build the .deb release package | [operations/packaging.md](operations/packaging.md) |
| Sharing/federation (in progress) | [features/sharing.md](features/sharing.md) |
| Dashboard layout + widgets | [features/dashboard.md](features/dashboard.md) |
| Dated review findings | [specs/review.md](specs/review.md) |
| Latest architecture/code review (2026-09-19) | [specs/review-2026-09-19.md](specs/review-2026-09-19.md) |
| In-flight designs | [plans/](plans/) |

## Sections

- **specs/**: authoritative references: the release plan, the reconciler spec,
  the app spec, and the dated review snapshot.
- **architecture/**: system design overview and data flows.
- **guides/**: how-to documentation.
- **features/**: per-feature documentation.
- **operations/**: maintenance and devOps, including the backend debt ledger.
- **plans/**: design plans. Every `plans/*.md` starts with
  `> Status: draft | accepted | landed | dropped`; landed plans move to
  `plans/archive/`.
