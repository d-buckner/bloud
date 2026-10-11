// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Reading the orchestrator's own state, for the one place the graph can say it:
 * the frame around the apps it is reconciling.
 *
 * Everything else about reconciliation belongs on a node: a phase, a failure, a
 * restart signal all name one thing the engine is holding, and the graph already
 * draws that thing. What no node can carry is the state of the loop itself,
 * because a dead loop leaves every node looking exactly as healthy as it did
 * when the loop was alive. That is the sentence this module exists to put on the
 * frame, and it is the reason the frame is the last overlay left.
 */
import type { OrchestratorStatus } from '$lib/clients/developerClient';

export type EngineTone = 'error' | 'info' | 'warn' | 'idle';

export interface FrameStatus {
	/** Empty when the frame has nothing to add to the word "Installed apps". */
	note: string;
	tone: EngineTone;
}

/**
 * How old a pass has to be before its age is worth printing.
 *
 * The self-heal timer fires on an idle ~60s, so a fresh pass is unremarkable and
 * printing "last pass 40s ago" is a clock ticking for no reason. Five minutes
 * means several passes were due and none landed, which is news.
 */
const STALE_AFTER_MS = 5 * 60_000;

/** "42s ago", stepping the unit up as the gap grows; "never" when unreadable. */
function relativeTime(iso: string | undefined, now: number): string {
	const at = Date.parse(iso ?? '');
	if (Number.isNaN(at)) return 'never';
	const seconds = Math.max(0, Math.floor((now - at) / 1000));
	if (seconds < 60) return `${seconds}s ago`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m ago`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours}h ago`;
	return `${Math.floor(hours / 24)}d ago`;
}

/**
 * What the frame around the installed apps should say about the reconciler.
 *
 * Quiet by design: an idle engine gets no note at all, because the frame's job
 * is to be the thing that changes when the engine stops being boring.
 */
export function frameStatus(status: OrchestratorStatus | undefined, now: number): FrameStatus {
	if (!status) return { note: 'reconciler unavailable', tone: 'error' };
	// A dead loop accepts every submit and reconciles nothing, which from the
	// outside looks exactly like an idle engine. It outranks everything else.
	if (status.loopStopped) return { note: 'reconciler stopped', tone: 'error' };
	if (status.isConverging) {
		const queued = status.queueDepth > 0 ? ` · ${status.queueDepth} queued` : '';
		return { note: `converging${queued}`, tone: 'info' };
	}
	if (status.queueDepth > 0) {
		return { note: `${status.queueDepth} queued`, tone: 'info' };
	}
	const last = Date.parse(status.lastConverged ?? '');
	if (Number.isNaN(last)) return { note: 'no pass yet', tone: 'warn' };
	if (now - last > STALE_AFTER_MS) {
		return { note: `last pass ${relativeTime(status.lastConverged, now)}`, tone: 'warn' };
	}
	return { note: '', tone: 'idle' };
}
