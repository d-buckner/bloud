// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Wire types - the payloads the server puts on the event stream.
 *
 * These are the client's copy of a server contract, not a client invention.
 * `PHASE_ORDER` mirrors `phaseForStatus` in
 * `services/host-agent/internal/engine/orchestrator/status.go`: the engine
 * emits queued/configuring/starting/finalizing/running/failed and the client
 * adds `pulling`, which only the client can know (it comes from the image-pull
 * stream, and the engine has no pull phase).
 */

/** One graph transition for one container. */
export interface NodeEvent {
	app: string;
	container: string;
	phase: string;
	error?: string;
}

/** Image pull progress for one image of one app. */
export interface PullEvent {
	app: string;
	image: string;
	phase: 'pulling' | 'done';
	percent?: number;
	detail?: string;
}

/** One entry of the orchestrator activity ring. */
export interface ActivityEvent {
	time: string;
	event: string;
	detail: string;
}

/** User-facing phase order, earliest to done. `failed` ranks above all. */
export const PHASE_ORDER = [
	'queued',
	'pulling',
	'configuring',
	'starting',
	'finalizing',
	'running'
] as const;

/** How many phase transitions the client tracks per app for the timeline. */
export const MAX_PHASE_HISTORY = 12;
