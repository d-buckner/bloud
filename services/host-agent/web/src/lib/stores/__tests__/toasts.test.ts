// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, it, expect } from 'vitest';
import { detectToasts } from '../toasts';
import type { App } from '$lib/types';

function app(catalog_id: string, status: string, display_name?: string): App {
	return {
		id: 1,
		catalog_id,
		display_name: display_name ?? catalog_id,
		version: '1',
		status: status as App['status'],
		is_system: false,
		installed_at: '2026-01-01',
		updated_at: '2026-01-01'
	};
}

describe('detectToasts', () => {
	it('toasts when an app reaches running from a non-running status', () => {
		const prev = new Map([['jellyfin', 'installing']]);
		const toasts = detectToasts(prev, [app('jellyfin', 'running', 'Jellyfin')]);
		expect(toasts).toEqual([{ message: 'Jellyfin is ready', tone: 'success' }]);
	});

	it('does not re-toast an app that is already running', () => {
		const prev = new Map([['jellyfin', 'running']]);
		expect(detectToasts(prev, [app('jellyfin', 'running')])).toEqual([]);
	});

	it('does not toast a freshly-seen app that is already running (no prev status)', () => {
		expect(detectToasts(new Map(), [app('jellyfin', 'running')])).toEqual([]);
	});

	it('toasts when an app enters failed', () => {
		const prev = new Map([['jellyfin', 'starting']]);
		const toasts = detectToasts(prev, [app('jellyfin', 'failed', 'Jellyfin')]);
		expect(toasts).toEqual([{ message: 'Jellyfin failed — view details', tone: 'error' }]);
	});

	it('does not re-toast while the app stays failed', () => {
		const prev = new Map([['jellyfin', 'failed']]);
		expect(detectToasts(prev, [app('jellyfin', 'failed')])).toEqual([]);
	});

	it('deliberately stays quiet for degraded (error) status', () => {
		const prev = new Map([['jellyfin', 'running']]);
		expect(detectToasts(prev, [app('jellyfin', 'error')])).toEqual([]);
	});

	it('detects multiple transitions in one snapshot', () => {
		const prev = new Map([
			['jellyfin', 'installing'],
			['navidrome', 'starting'],
			['immich', 'running']
		]);
		const toasts = detectToasts(prev, [
			app('jellyfin', 'running'),
			app('navidrome', 'failed'),
			app('immich', 'running')
		]);
		expect(toasts).toEqual([
			{ message: 'jellyfin is ready', tone: 'success' },
			{ message: 'navidrome failed — view details', tone: 'error' }
		]);
	});
});
