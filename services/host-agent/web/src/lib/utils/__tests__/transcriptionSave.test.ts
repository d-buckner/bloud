// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The two save rules the browser used to be the only witness of.
 *
 * What a form sends and what counts as stored are both decisions, and both were
 * only checkable by driving a page against a running orchestrator. That made the
 * slow path the only test: a wrong answer took a browser, an install, and a
 * convergence pass to surface, and reported itself as a mismatched string.
 */

import { describe, expect, it } from 'vitest';
import type {
	SetTranscriptionResponse,
	TranscriptionSettings
} from '$lib/clients/settingsClient';
import { appliedTo, buildSaveRequest, type TranscriptionForm } from '../transcriptionSave';

const FORM: TranscriptionForm = {
	name: '  Lab  ',
	baseUrl: '  https://tr.example.test/v1  ',
	model: '  whisper-x  ',
	apiKey: ''
};

const ACCEPTED: SetTranscriptionResponse = {
	status: 'accepted',
	baseUrl: 'https://tr.example.test/v1',
	model: 'whisper-x',
	configured: true
};

const LIVE: TranscriptionSettings = {
	configured: true,
	name: 'Lab',
	baseUrl: 'https://tr.example.test/v1',
	model: 'whisper-x',
	hasApiKey: false,
	servedTo: ['podly']
};

describe('buildSaveRequest', () => {
	it('sends the fields trimmed, so a stray space is not stored', () => {
		expect(buildSaveRequest(FORM, false)).toEqual({
			baseUrl: 'https://tr.example.test/v1',
			name: 'Lab',
			model: 'whisper-x'
		});
	});

	it('omits a blank key, because omitting means keep the stored one', () => {
		expect(buildSaveRequest({ ...FORM, apiKey: '' }, false).apiKey).toBeUndefined();
	});

	it('sends a key the operator typed', () => {
		expect(buildSaveRequest({ ...FORM, apiKey: 'sk-1' }, false).apiKey).toBe('sk-1');
	});

	it('falls back to the default name when the field is blank', () => {
		expect(buildSaveRequest({ ...FORM, name: '  ' }, false).name).toBe('Transcription');
	});

	it('a remove sends no address and never a credential', () => {
		const body = buildSaveRequest({ ...FORM, apiKey: 'sk-1' }, true);
		expect(body.baseUrl).toBe('');
		expect(body.apiKey).toBeUndefined();
	});
});

describe('appliedTo', () => {
	it('is not settled while the stored endpoint is still the previous one', () => {
		const settled = appliedTo(ACCEPTED, undefined);
		expect(settled({ ...LIVE, baseUrl: '' })).toBe(false);
		expect(settled({ ...LIVE, model: '' })).toBe(false);
		expect(settled({ ...LIVE, configured: false })).toBe(false);
	});

	it('settles once every field the save reported matches', () => {
		expect(appliedTo(ACCEPTED, undefined)(LIVE)).toBe(true);
	});

	it('ignores the credential when the save carried none', () => {
		expect(appliedTo(ACCEPTED, undefined)({ ...LIVE, hasApiKey: true })).toBe(true);
	});

	it('waits for a key that was sent, and for one that was cleared', () => {
		expect(appliedTo(ACCEPTED, 'sk-1')({ ...LIVE, hasApiKey: false })).toBe(false);
		expect(appliedTo(ACCEPTED, 'sk-1')({ ...LIVE, hasApiKey: true })).toBe(true);
		expect(appliedTo(ACCEPTED, '')({ ...LIVE, hasApiKey: true })).toBe(false);
	});

	it('settles a remove once nothing is configured', () => {
		const removed: SetTranscriptionResponse = {
			status: 'accepted',
			baseUrl: '',
			model: '',
			configured: false
		};
		const empty: TranscriptionSettings = {
			configured: false,
			name: '',
			baseUrl: '',
			model: '',
			hasApiKey: false,
			servedTo: []
		};
		expect(appliedTo(removed, undefined)(empty)).toBe(true);
		expect(appliedTo(removed, undefined)(LIVE)).toBe(false);
	});
});
