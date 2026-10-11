// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Node status → color mapping for the developer graph. Extracted from
 * AppNode.svelte so the mapping is a plain lookup (no branch fan-out) and
 * is unit-testable in isolation.
 *
 * The colors are the design system's tokens, not a graph-local palette, and
 * that is a contrast decision rather than a tidiness one. A status dot is a
 * graphic that carries meaning on its own, so WCAG 1.4.11 wants 3:1 against the
 * card behind it, and the phase word next to it wants 4.5:1 as text. The
 * brighter tailwind-style colors this used to return do not clear those bars:
 * #eab308 on white is 1.9:1, which is a dot that reads as a smudge, and
 * #9ca3af is 2.5:1. Every token below sits at 7:1 or better on white, so the
 * same value can be used for the dot and for the word beside it.
 */

/** Gray for idle/queued states and any unknown status: neutral, not "off". */
const DEFAULT_COLOR = 'var(--color-text-secondary)';

/** Explicit status → color table; anything not listed falls back to gray. */
const STATUS_COLORS: Record<string, string> = {
	running: 'var(--color-success)',
	active: 'var(--color-success)',
	error: 'var(--color-error)',
	failed: 'var(--color-error)',
	exited: 'var(--color-error)',
	dead: 'var(--color-error)',
	healthcheck: 'var(--color-warning)',
	starting: 'var(--color-warning)',
	prestart: 'var(--color-info)',
	poststart: 'var(--color-info)',
	configuring: 'var(--color-info)',
	finalizing: 'var(--color-info)',
	queued: DEFAULT_COLOR,
	installing: DEFAULT_COLOR,
};

/** Map an orchestrator/container status string to its display color. */
export function statusColor(status: string): string {
	return STATUS_COLORS[status] ?? DEFAULT_COLOR;
}
