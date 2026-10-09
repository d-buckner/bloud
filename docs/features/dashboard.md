# Dashboard

The home screen (`/`) is the app launcher plus optional widgets, on a single
drag-and-drop grid.

## Layout model

- **One grid, two element kinds.** Apps are fixed 1x1 tiles. Widgets take their
  default size from `src/lib/widgets/registry.ts` (`size` is the default *and*
  the minimum, `maxSize` the ceiling). Both live in one GridStack grid, six
  columns wide at desktop.
- **Positions are server-owned.** `GET /api/user/home` returns apps and widgets
  with `x/y/w/h`; `PUT /api/user/layout` replaces the user's whole position set
  (`store.SetForUser` deletes then inserts). The frontend never invents
  positions except when adding an element, and the settled grid is always sent
  back in full.
- **A widget exists only if it has a position row.** There is no separate
  "enabled widgets" record: toggling a widget in the picker inserts or removes
  its row. Clearing the layout therefore clears the widgets too.
- **The layout is authored at desktop width.** Narrower viewports get fewer
  columns (4 below ~820px of grid width, 2 below ~560px) as a *read-only
  preview*: drag and resize are disabled, the preview is laid out densely from
  scratch, and nothing is persisted. Widening returns to the stored layout
  exactly, because the store, not the compressed view, stays authoritative.
- **New widgets open a block below the app tiles** rather than filling the first
  gap among them (`firstFreeSlot` in `src/lib/utils/gridPlacement.ts`), so apps
  and widgets do not interleave into a staircase. GridStack may still pack the
  block upward into a gap left in the app rows.
- **Structural changes persist explicitly.** GridStack swallows its `change`
  event while a `batchUpdate` is open, so install/uninstall/widget-toggle save
  the layout directly instead of waiting for an event that never fires. Only
  user drags and resizes rely on `change`.

## Status on a tile

`running` opens the app; `installing`/`starting`/`failed`/`error` open the
install detail modal, because investigation is the point. The tile shows only
the app name while installing; the icon carries the spinner, and live phase
detail lives in the modal.

## External tiles

A launcher or a remote install of a catalog app renders as a `LauncherTile`
and is not an installed app, so it gets its own affordances rather than the
lifecycle ones:

- **Add** lives in the page header grouped with **Add widget** in one action
  cluster, not in the catalog. An external app is not a catalog entry, and
  putting it in the catalog list made it look like one: the same card shape,
  the same place you go to install, and no install behind it.
- **Right-click** opens a menu with **Configure** and **Remove**. Configure
  edits the record the operator declared: name and endpoint always, the icon
  for a launcher, and for a remote install the credentials and values the
  provider's `provides:` does not already answer, generated the same way the
  add form generates them. The record's kind and source are not editable:
  changing what a record *is* means removing it and adding the other thing.
- **Remove** confirms first, and the confirmation says what it does not do:
  nothing on the remote machine is touched, so a credential Bloud minted
  over there stays until it is revoked in that app's own settings.

The config form never has to retype a stored credential. The list endpoint
does not echo secrets, so a blank secret is dropped from the PATCH body rather
than sent empty: omitted means "keep what is on file", and blank reads as a
credential the operator typed. That rule lives in
`src/lib/utils/externalAppPatch.ts` and is unit-tested, because the two
words "omitted" and "blank" are the whole difference between a save that
works and one that wipes a credential.

A bare contract provider (an AI upstream, owned by Settings -> AI) never
reaches the grid: it is an endpoint, not a destination, so it has no tile to
right-click.

## Files

|File|Role|
|---|---|
|`src/routes/+page.svelte`|Page shell: header, toolbar, empty/loading/error states, modals|
|`src/lib/components/GridStackGrid.svelte`|GridStack instance, mounting, sync, persistence|
|`src/lib/components/AppTile.svelte`|App tile (`app-slot`, `install-spinner`, `phase-label` are e2e contracts)|
|`src/lib/components/LauncherTile.svelte`|Tile for a launcher or a remote install|
|`src/lib/components/ExternalAppContextMenu.svelte`|Right-click menu for an external tile|
|`src/lib/components/ExternalAppConfigModal.svelte`|Edit one external record: endpoint, values, credentials|
|`src/lib/components/ExternalAppRemoveModal.svelte`|Confirm dropping an external record|
|`src/lib/utils/externalAppPatch.ts`|PATCH body rules for an external record (pure, unit-tested)|
|`src/lib/stores/grid.ts`|Elements in the grid, from the home snapshot|
|`src/lib/utils/gridDiff.ts`|Store-vs-grid diff (pure, unit-tested)|
|`src/lib/utils/gridPlacement.ts`|Free-slot search (pure, unit-tested)|
|`src/lib/widgets/`|Widget registry, chrome (`Widget.svelte`), picker, widget components|

## Adding a widget

1. Add the component under `src/lib/widgets/`.
2. Add a registry entry: `id`, `name`, `description`, `icon`, `component`,
   `size`, `maxSize`.
3. Add `static/icons/<icon>.svg` (a 24x24 stroke SVG; the icon is used as a CSS
   mask, so it must be monochrome with `currentColor`).

A stored position whose id is not in the registry is dropped from the grid, so
removing a widget from the registry is safe.
