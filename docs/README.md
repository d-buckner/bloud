# Documentation Index

## Structure

```
docs/
├── architecture/          # System architecture reference
│   └── architecture.md    # How Bloud works end-to-end
├── designs/              # Feature specs, plans, and proposals
│   ├── cli-backend-abstraction.md     # Backend abstraction design
│   ├── multi-container-apps.md        # Multi-container app format
│   ├── persistent-remote-access.md    # Tailscale gateway + owner access
│   └── sharing.md                     # Federated sharing architecture
├── guides/              # Developer reference and how-tos
│   └── contributing-apps.md           # How to add a new app
└── tracking/           # Active inventories (tech debt, etc.)
    └── backend-tech-debt.md
```

## Categories

- **architecture/** — canonical reference for how Bloud works. The "what" and "why."
- **designs/** — feature specs and implementation plans. The "how" for specific features.
- **guides/** — procedural docs for contributors. How to do things.
- **tracking/** — living inventories that get updated as work progresses.

## Cross-references

When referencing other docs, use the category prefix:

```markdown
- [architecture/architecture.md](../architecture/architecture.md)
- [designs/sharing.md](../designs/sharing.md)
- [guides/contributing-apps.md](../guides/contributing-apps.md)
- [tracking/backend-tech-debt.md](../tracking/backend-tech-debt.md)
```

## File naming

- kebab-case, topic-only
- No prefixes like "plan-" or "-spec" — the folder implies the type
- Short and clear: `sharing.md`, not `sharing-architecture-design.md`
