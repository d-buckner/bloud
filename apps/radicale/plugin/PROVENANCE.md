# Vendored: radicale-ics-sync

`radicale_ics_sync/` is a vendored copy of the MIT-licensed
[`radicale-ics-sync`](https://pypi.org/project/radicale-ics-sync/) storage
plugin, version 0.1.0. It is vendored rather than built into a custom Radicale
image so Bloud ships no image pipeline of its own: the sources are embedded in
the host-agent binary, written into `<appDataDir>/plugin` by the Radicale
configurator, mounted read-only at `/plugins`, and made importable with
`PYTHONPATH=/plugins`.

- Upstream LICENSE: `LICENSE` in this directory (kept verbatim).
- Upstream source: `radicale_ics_sync-0.1.0-py3-none-any.whl`.
- Pinned version: 0.1.0.

## Local modifications

Two changes are carried on top of upstream, both in
`radicale_ics_sync/storage.py`. Re-apply them when the vendored copy is
updated.

1. **Create the target collection when it is missing.** Upstream requires the
   collection to already exist and otherwise logs "create it in the Radicale
   web UI first" and skips the feed. Bloud cannot create it over the DAV API,
   because the collection belongs to a Bloud user whose password the host agent
   never holds, so there is no client credential to `MKCALENDAR` with. The
   plugin runs inside Radicale's storage layer and already has write access, so
   `_ensure_collection` creates a `VCALENDAR` collection (named from the
   `displayname` field of the sync job) before the first sync. Lines added:
   `_ensure_collection` and the `_sync_events` call to it.

2. **Persist the upstream hash database inside the mounted tree.** Upstream
   writes `ics_sync_hashes.json` to `dirname(filesystem_folder)`, which in this
   image is `/var/lib/radicale` and is not a bind mount, so it is lost on every
   container recreate. A lost hash database re-uploads every event and never
   deletes the ones that disappeared upstream, so the plugin gains an optional
   `hash_db` `[storage]` key and Radicale's generated config points it at
   `/var/lib/radicale/collections/ics_sync_hashes.json`, inside the persisted
   collections tree. Lines added: the `hash_db` schema entry and the
   `_hash_db_path` branch in `Storage.__init__`.
