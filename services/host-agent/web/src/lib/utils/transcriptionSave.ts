// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The two rules a transcription save turns on, kept apart from the form.
 *
 * Both are the parts that were only observable through a browser: what leaves the
 * page for a given form state, and when the stored state counts as having caught
 * up with the save. A settings save is an intent, so "saved" is a claim about the
 * future, and a claim that needs a definition a test can check without waiting on
 * an orchestrator to apply something.
 */

import type { PollWindow } from '$lib/store/effects/convergence';
import type {
	SetTranscriptionRequest,
	SetTranscriptionResponse,
	TranscriptionSettings
} from '$lib/clients/settingsClient';

/**
 * How long a save is given to land, and how often to look.
 *
 * Half a second is the same cadence the address setting polls at: fast enough
 * that a healthy save is settled before the operator finishes reading the toast,
 * slow enough that a slow orchestrator is not hammered. Twenty seconds is well
 * past the time an intent takes to reach the front of the queue on a busy box.
 */
export const SAVE_WINDOW: PollWindow = { intervalMs: 500, timeoutMs: 20_000 };

/** The form fields a save is built from, as the operator left them. */
export interface TranscriptionForm {
	name: string;
	baseUrl: string;
	model: string;
	apiKey: string;
}

/**
 * The request body for the current form state.
 *
 * A remove sends an empty address and never a credential: removing the provider
 * deletes its key with it, so sending one alongside the remove would race the
 * delete. A blank key field is omitted rather than sent blank, because omitting
 * means "keep the stored key" and an empty string means "clear it".
 */
export function buildSaveRequest(
	form: TranscriptionForm,
	remove: boolean
): SetTranscriptionRequest {
	const body: SetTranscriptionRequest = {
		baseUrl: remove ? '' : form.baseUrl.trim(),
		name: form.name.trim() || 'Transcription',
		model: form.model.trim()
	};
	if (!remove && form.apiKey !== '') {
		body.apiKey = form.apiKey;
	}
	return body;
}

/**
 * Whether the stored state has reached what this save asked for.
 *
 * The credential is only part of the question when the save carried one: an
 * omitted key means "keep the stored key", so its presence is not this save's to
 * wait for, and waiting on it would stall every save that did not type one.
 */
export function appliedTo(
	accepted: SetTranscriptionResponse,
	sentKey: string | undefined
): (live: TranscriptionSettings) => boolean {
	return (live) =>
		live.baseUrl === accepted.baseUrl &&
		live.model === accepted.model &&
		live.configured === accepted.configured &&
		(sentKey === undefined || live.hasApiKey === (sentKey !== ''));
}
