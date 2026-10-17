# Time zone

One setting sets the clock for the whole instance. The operator picks an IANA
zone under Settings → Time zone, and Bloud writes it into every container it
manages.

## What it does

Bloud adds `TZ=<zone>` to the environment of every managed container. System
infrastructure is included: Traefik and Authentik get the zone with everything
else.

The platform value wins over anything an app declares. An app's `metadata.yaml`
should not set `TZ`. The catalog no longer carries one, because two sources for
one clock produce a value nobody can predict.

When no zone is set, Bloud writes nothing. Each image then keeps its own default,
which is UTC for most images. An unset zone is the state a fresh instance starts
in, and it is a value in its own right: it is not a hidden UTC.

## Why the platform owns it

An app reads the clock for its own schedules. Sonarr runs a search at 03:00, and
03:00 means a different moment in every zone. Before this setting, each image
decided for itself, so an operator who wanted the apps on local time had to edit
each app's environment by hand and lose the change on the next reinstall.

## Changing the zone recreates the containers

A container's environment is fixed when the runtime creates it. A new zone
therefore cannot reach a running container. When the saved zone changes, the
orchestrator resets every running node, and the next pass recreates the
containers with the new `TZ`.

The apps restart. Their data is untouched. Saving the zone that is already in
force does nothing: the orchestrator refuses a change that would change nothing,
so a form that submits twice cannot restart the stack twice.

The dashboard states the restart before the operator agrees to it, and the Save
button stays disabled until the operator checks the acknowledgement.

## The API

| Method and path | Behavior |
|---|---|
| `GET /api/settings/timezone` | Returns `{ "zone": "Europe/Berlin" }`, or `{ "zone": "" }` when no zone is set. |
| `PUT /api/settings/timezone` | Accepts `{ "zone": "<IANA name>" }`, or `{ "zone": "" }` to clear it. Returns 202 with the intent id and the zone the request was accepted for. |

The write is an intent. The 202 means the orchestrator took the change, not that
the containers carry it yet. Read `GET /api/settings/timezone` to see the value
in force.

## What the setting accepts

The name must resolve as a location in the IANA time zone database. The
host-agent carries its own copy of that database, so validation does not depend
on what the host has under `/usr/share/zoneinfo`.

The setting rejects:

- a name that does not resolve, for example `Mars/Olympus`
- `Local`, which means the container's own clock rather than a chosen zone
- a name longer than 64 characters, or one that contains a path separator or a
  `..` segment

An empty string is not a rejection. It clears the setting, and the containers
lose `TZ` on the next pass.

## Where it lives

| Path | Role |
|---|---|
| `services/host-agent/internal/timezone/` | The parse rule and the live value. |
| `services/host-agent/internal/api/settings_timezone.go` | The two handlers. |
| `applySetTimezoneIntent` in `internal/engine/orchestrator/pipeline_intents.go` | Persist, publish, reset. |
| `applyTimezone` in `internal/engine/orchestrator/execution.go` | Writes `TZ` into each container spec. |
| `TimezoneSettingsSection.svelte` | The form. |

The stored key is `settings['timezone']`. The live value is a
`timezone.State` the orchestrator owns, so building a container spec reads memory
rather than the database.
