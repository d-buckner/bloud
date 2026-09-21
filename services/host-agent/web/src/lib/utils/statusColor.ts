// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Node status → color mapping for the developer graph. Extracted from
 * AppNode.svelte so the mapping is a plain lookup (no branch fan-out) and
 * is unit-testable in isolation.
 */

/** Gray used for idle/queued states and any unknown status. */
const DEFAULT_COLOR = '#9ca3af';

/** Explicit status → color table; anything not listed falls back to gray. */
const STATUS_COLORS: Record<string, string> = {
	running: '#16a34a',
	active: '#16a34a',
	error: '#dc2626',
	failed: '#dc2626',
	exited: '#dc2626',
	dead: '#dc2626',
	healthcheck: '#eab308',
	starting: '#eab308',
	prestart: '#3b82f6',
	poststart: '#3b82f6',
	configuring: '#3b82f6',
	finalizing: '#3b82f6',
	queued: DEFAULT_COLOR,
	installing: DEFAULT_COLOR,
};

/** Map an orchestrator/container status string to its display color. */
export function statusColor(status: string): string {
	return STATUS_COLORS[status] ?? DEFAULT_COLOR;
}
