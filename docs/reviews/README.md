# Dated reviews

Every full review of this codebase, oldest first.

A review is a snapshot. It records what was true on its date, against the commit
named in its header. Do not add a finding to a review. New findings go to
[`docs/operations/tech-debt.md`](../operations/tech-debt.md), the one living
ledger for backend debt. Each review carries a status table that names the
findings repaid since it was written, so a reader does not repay a debt that is
already paid.

| Review | Date | Scope | What it found |
|---|---|---|---|
| [2026-09-17.md](2026-09-17.md) | 2026-09-17 | High-level design and architecture | The first full audit. It named the inert install path (C1) and the hardcoded fallback secrets (C4). Both are closed. |
| [2026-09-19.md](2026-09-19.md) | 2026-09-19 | The whole repository: runtime, apps, CLI, frontend, CI, docs | Nine parallel subsystem audits. The P0 was a remote admin bypass through one spoofed header, closed the same day. |
| [2026-10-03-aposd.md](2026-10-03-aposd.md) | 2026-10-03 | The Go runtime, read through *A Philosophy of Software Design* (APoSD) | Module depth and information hiding in the runtime. Its findings became ledger items 26 to 34, and eight of the ten are closed. |
| [2026-10-12-aposd-appsurface.md](2026-10-12-aposd-appsurface.md) | 2026-10-12 | The app-extension surface an app author compiles against | It asked whether the app registries should have to exist at all, after [app-add-tax.md](../plans/archive/app-add-tax.md) removed them. |

## Naming

One file per review, named for its date: `YYYY-MM-DD.md`. Add a short suffix only
where it separates two reviews on the same day or names the lens in use, as
`-aposd` does. The directory says what the file is, so a file name does not
repeat it.
