// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The action types, alone in their own module on purpose.
 *
 * `actions.ts` derives `KNOWN_ACTION_TYPES` from `Object.values` of this file.
 * That only works if this module exports nothing but types, and it is the reason
 * the list cannot drift: a new constant is in the set the moment it exists, with
 * no second place to remember.
 *
 * The alternative, an array of names maintained by hand next to the constants it
 * is supposed to mirror, is the shape that goes stale silently.
 *
 * A value here that no reducer implements is also dead vocabulary, and
 * `__tests__/actions.test.ts` fails on one: it greps every slice and the
 * middleware for the constant's name. That direction cannot be checked from
 * inside the module, which is why it is a test and not a type.
 */

export const SESSION_BOOTSTRAP_STARTED = 'session/bootstrapStarted';
export const SESSION_BOOTSTRAP_LOADED = 'session/bootstrapLoaded';
export const SESSION_BOOTSTRAP_FAILED = 'session/bootstrapFailed';
export const SESSION_SIGNED_OUT = 'session/signedOut';
export const CONNECTION_EVENT = 'connection/event';
export const CONNECTION_RESET = 'connection/reset';
export const HOME_SNAPSHOT = 'home/snapshot';
export const APPS_UPSERTED = 'apps/upserted';
export const APPS_STATUS_PATCHED = 'apps/statusPatched';
export const APPS_READ_FAILED = 'apps/readFailed';
export const LAYOUT_ELEMENT_ADDED = 'layout/elementAdded';
export const LAYOUT_ELEMENT_REMOVED = 'layout/elementRemoved';
export const LAYOUT_POSITIONS_SET = 'layout/positionsSet';
export const LAUNCHERS_REPLACED = 'launchers/replaced';
export const GRAPH_LOADED = 'graph/loaded';
export const GRAPH_FAILED = 'graph/failed';
export const GRAPH_RESET = 'graph/reset';
export const PROGRESS_NODE = 'progress/node';
export const PROGRESS_PULL = 'progress/pull';
export const PROGRESS_ACTIVITY = 'progress/activity';
export const TOAST_PUSHED = 'toast/pushed';
export const TOAST_DISMISSED = 'toast/dismissed';
export const COMMAND_STARTED = 'command/started';
export const COMMAND_SETTLED = 'command/settled';
export const COMMAND_FAILED = 'command/failed';
