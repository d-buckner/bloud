# Design: Unified Rebuild Log

> **Status: NOT IMPLEMENTED** - Proposed design for structured rebuild logging.

## Overview

A singular, structured log for each nixos-rebuild operation that serves as the source of truth for debugging. When an app isn't working or a rebuild fails, this log provides everything an investigation agent (or human) needs to identify the issue.

## Goals

- **Single source of truth** for rebuild debugging (no hunting across journalctl, stdout, etc.)
- **Agent-parseable** structure with clear phases and actionable error messages
- **Minimal noise** - capture what matters, skip routine success chatter
- **Persistent** - survives restarts, retains history for debugging intermittent issues

## Non-Goals

- Real-time streaming (SSE already handles this for UI)
- Replacing journalctl for individual app logs
- Log aggregation/shipping to external systems
- Detailed Nix build output parsing

---

## Current State

Logs are scattered across multiple sources:

| Source | What It Captures | Issues |
|--------|-----------------|--------|
| stdout (JSON via slog) | Host-agent operations | Not persisted, mixed with all other logs |
| nixos-rebuild output | Build output, service changes | Captured but only streamed, not stored |
| journalctl | Individual app service logs | Per-service, requires knowing which to check |
| SSE stream | Real-time rebuild events | Ephemeral, lost on disconnect |

**Problem**: When something fails, you need to check multiple places and correlate timestamps. An investigation agent has no single place to look.

---

## Proposed Design

### Log Structure

Each rebuild produces a single log file with phases:

```json
{
  "rebuild_id": "2026-01-14T10:23:45Z-abc123",
  "started_at": "2026-01-14T10:23:45Z",
  "trigger": "install",
  "trigger_context": {
    "apps": ["miniflux"],
    "source": "api"
  },
  "phases": [
    {
      "name": "preflight",
      "started_at": "...",
      "completed_at": "...",
      "status": "success",
      "entries": [...]
    },
    {
      "name": "nix_config",
      "started_at": "...",
      "completed_at": "...",
      "status": "success",
      "entries": [...]
    },
    {
      "name": "rebuild",
      "started_at": "...",
      "completed_at": "...",
      "status": "success",
      "entries": [...]
    },
    {
      "name": "post_rebuild",
      "started_at": "...",
      "completed_at": "...",
      "status": "success",
      "entries": [...]
    }
  ],
  "completed_at": "2026-01-14T10:24:12Z",
  "status": "success",
  "summary": {
    "duration_ms": 27000,
    "apps_affected": ["miniflux"],
    "services_started": ["podman-miniflux.service"],
    "services_failed": [],
    "errors": [],
    "warnings": []
  }
}
```

### Phases

| Phase | What Happens | What to Log |
|-------|-------------|-------------|
| **preflight** | Validate config, check dependencies | Missing deps, invalid config, blocked conditions |
| **nix_config** | Generate apps.nix, blueprints, traefik routes | Files written, generation errors |
| **rebuild** | Run `nixos-rebuild switch` | Command args, exit code, stderr on failure, service changes |
| **post_rebuild** | Daemon reload, health checks, configurators | Services started, health check failures, configurator errors |

### Entry Types

Each phase contains entries. Keep these minimal but useful:

```typescript
type LogEntry = {
  timestamp: string;
  level: "info" | "warn" | "error";
  message: string;
  context?: Record<string, unknown>;  // Structured data for agent parsing
}
```

**What TO log** (high signal):
- Phase transitions: `{ level: "info", message: "Starting nix config generation" }`
- Errors with context: `{ level: "error", message: "Blueprint generation failed", context: { app: "miniflux", error: "missing client_id" } }`
- Service state changes: `{ level: "info", message: "Service started", context: { service: "podman-miniflux.service" } }`
- Health check failures: `{ level: "warn", message: "Health check failed", context: { app: "miniflux", attempt: 3, error: "connection refused" } }`
- Final health check result: `{ level: "error", message: "Health check timeout", context: { app: "miniflux", attempts: 30, last_error: "..." } }`

**What NOT to log** (noise):
- Every health check attempt (just log failures and final result)
- Individual nixos-rebuild output lines (unless stderr on failure)
- Routine file writes (just log counts: "Wrote 3 config files")
- Polling/waiting loops

### Storage

**Location**: `{dataDir}/logs/rebuilds/`

**Naming**: `{timestamp}-{short_id}.json` (e.g., `2026-01-14T10-23-45Z-abc123.json`)

**Retention**: Keep last 50 rebuilds (configurable). Delete oldest on new rebuild.

**Index file**: `{dataDir}/logs/rebuilds/index.json` for quick lookup:
```json
{
  "latest": "2026-01-14T10-23-45Z-abc123",
  "rebuilds": [
    { "id": "...", "timestamp": "...", "status": "success", "trigger": "install", "apps": ["miniflux"] },
    { "id": "...", "timestamp": "...", "status": "failed", "trigger": "uninstall", "apps": ["postgres"] }
  ]
}
```

---

## Implementation Plan

### Phase 1: Core Logger

Create a rebuild log writer that phases can call into:

```go
// internal/rebuildlog/logger.go

type RebuildLogger struct {
    rebuildID string
    dataDir   string
    current   *RebuildLog
    mu        sync.Mutex
}

func (l *RebuildLogger) StartRebuild(trigger string, context map[string]any) string
func (l *RebuildLogger) StartPhase(name string)
func (l *RebuildLogger) Log(level, message string, context map[string]any)
func (l *RebuildLogger) EndPhase(status string)
func (l *RebuildLogger) Complete(status string) error  // Writes to disk
```

### Phase 2: Integrate with Orchestrator

Update `orchestrator_nix.go` to use the rebuild logger:

```go
func (o *NixOrchestrator) InstallApp(ctx context.Context, name string, ...) error {
    rebuildID := o.rebuildLog.StartRebuild("install", map[string]any{"apps": []string{name}})
    defer o.rebuildLog.Complete(...)

    // Preflight
    o.rebuildLog.StartPhase("preflight")
    if err := o.validateInstall(name); err != nil {
        o.rebuildLog.Log("error", "Preflight validation failed", map[string]any{"error": err.Error()})
        o.rebuildLog.EndPhase("failed")
        return err
    }
    o.rebuildLog.EndPhase("success")

    // Continue for each phase...
}
```

### Phase 3: Capture Rebuild Output

Update `rebuild.go` to log errors to the rebuild log:

```go
func (r *Rebuilder) Switch(ctx context.Context) RebuildResult {
    // ... existing code ...

    // On stderr, log to rebuild log
    if line != "" && isStderr {
        r.rebuildLog.Log("error", "nixos-rebuild stderr", map[string]any{"line": line})
    }

    // On failure, include full stderr in context
    if !result.Success {
        r.rebuildLog.Log("error", "nixos-rebuild failed", map[string]any{
            "exit_code": exitCode,
            "stderr": stderrBuffer.String(),
        })
    }
}
```

### Phase 4: Health Check Logging

Update health check loops to log only failures and final result:

```go
func (o *NixOrchestrator) waitForHealthy(ctx context.Context, app string) error {
    var lastErr error
    for attempt := 1; attempt <= maxAttempts; attempt++ {
        if err := o.checkHealth(app); err != nil {
            lastErr = err
            // Only log every 10th failure to avoid spam
            if attempt%10 == 0 {
                o.rebuildLog.Log("warn", "Health check still failing", map[string]any{
                    "app": app, "attempt": attempt, "error": err.Error(),
                })
            }
            continue
        }
        o.rebuildLog.Log("info", "Health check passed", map[string]any{"app": app, "attempts": attempt})
        return nil
    }
    o.rebuildLog.Log("error", "Health check timeout", map[string]any{
        "app": app, "attempts": maxAttempts, "last_error": lastErr.Error(),
    })
    return lastErr
}
```

### Phase 5: API Endpoint

Add endpoint to retrieve rebuild logs:

```go
// GET /api/rebuilds - List recent rebuilds
// GET /api/rebuilds/{id} - Get specific rebuild log
// GET /api/rebuilds/latest - Get most recent rebuild log
```

---

## Agent Investigation Flow

When an agent needs to debug an issue:

1. **Fetch latest rebuild**: `GET /api/rebuilds/latest`
2. **Check overall status**: If `status: "failed"`, look at `summary.errors`
3. **Identify failing phase**: Find phase with `status: "failed"`
4. **Read phase entries**: Look for `level: "error"` entries with context
5. **Cross-reference**: Use app name from context to check app-specific logs if needed

Example agent prompt:
> "The user reports miniflux isn't working. Check the latest rebuild log at /api/rebuilds/latest. Look for errors related to miniflux in any phase. If the rebuild succeeded, check the post_rebuild phase for health check failures."

---

## What Makes a Good Log Entry

**Good** (actionable, contextual):
```json
{
  "level": "error",
  "message": "Blueprint generation failed for OAuth provider",
  "context": {
    "app": "miniflux",
    "provider": "miniflux-oauth",
    "error": "Authentik API returned 401: invalid token"
  }
}
```

**Bad** (vague, no context):
```json
{
  "level": "error",
  "message": "Failed to configure app"
}
```

**Guidelines**:
- Include the app name in context when relevant
- Include the actual error message, not just "failed"
- Include identifiers (service names, provider names) for cross-referencing
- Avoid generic messages that could apply to anything

---

## Open Questions

1. **Log rotation strategy?**
   - Keep last N logs (simple, proposed above)
   - Keep logs for N days
   - Keep until size limit reached
   - Recommendation: Start with last 50, add time-based later if needed

2. **Include full nixos-rebuild output?**
   - Pro: Complete record, useful for obscure failures
   - Con: Can be very long (megabytes), mostly noise
   - Recommendation: Only include stderr on failure, not full stdout

3. **Real-time append vs write-on-complete?**
   - Append: See partial progress if process crashes
   - Complete: Simpler, atomic writes
   - Recommendation: Write phases incrementally, final summary on complete

4. **Expose in UI?**
   - A "Rebuild History" view could be useful
   - Start with API-only, add UI later if useful
   - Recommendation: API first, UI is nice-to-have

---

## Success Criteria

After implementation, debugging a failed install should be:

1. **One command**: `curl localhost:3000/api/rebuilds/latest | jq`
2. **Clear failure point**: Phase name + error message immediately visible
3. **Actionable**: Error context includes enough to understand what went wrong
4. **Agent-friendly**: An AI agent can parse the JSON and identify the issue

---

## Future Extensions

- **Diff with previous**: Show what changed between rebuilds
- **Correlation IDs**: Link rebuild log entries to journalctl entries
- **Metrics**: Track rebuild duration trends, failure rates
- **Alerts**: Notify on repeated failures of same type
