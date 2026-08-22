// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, it, expect } from 'vitest';
import {
	emptyProgress,
	mergeNodeEvent,
	mergePullEvent,
	mergeSnapshot,
	type NodeEvent
} from '../appProgress';
import type { AppStatus, HomeData } from '$lib/types';

const NOW = 1_700_000_000_000;

function node(app: string, container: string, phase: string, error?: string): NodeEvent {
	return { app, container, phase, error };
}

const home = (statuses: Record<string, AppStatus>): HomeData => ({
	apps: Object.entries(statuses).map(([id, status]) => ({
		id: 1,
		catalog_id: id,
		display_name: id,
		version: '1',
		status,
		is_system: false,
		installed_at: '2026-01-01',
		updated_at: '2026-01-01',
		x: null,
		y: null,
		w: 1,
		h: 1
	})),
	widgets: []
});

describe('mergeNodeEvent', () => {
	it('records a single-container phase', () => {
		const m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'pulling'), NOW);
		expect(m.jellyfin.phase).toBe('pulling');
		expect(m.jellyfin.components['apps-jellyfin'].phase).toBe('pulling');
		expect(m.jellyfin.phaseHistory).toEqual([{ phase: 'pulling', at: NOW }]);
	});

	it('captures the error and keeps the pre-failure phase for the timeline', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'starting'), NOW);
		m = mergeNodeEvent(m, node('jellyfin', 'apps-jellyfin', 'failed', 'health check failed'), NOW + 1000);
		expect(m.jellyfin.phase).toBe('failed');
		expect(m.jellyfin.lastPhase).toBe('starting');
		expect(m.jellyfin.error).toBe('health check failed');
	});

	it('aggregates multi-container phases to the remaining work', () => {
		let m = mergeNodeEvent({}, node('immich', 'apps-immich-postgres', 'running'), NOW);
		m = mergeNodeEvent(m, node('immich', 'apps-immich-server', 'starting'), NOW + 100);
		// The app is still installing: the not-yet-finished container wins.
		expect(m.immich.phase).toBe('starting');
		expect(Object.keys(m.immich.components)).toHaveLength(2);

		// Once every container is running, the app is ready.
		m = mergeNodeEvent(m, node('immich', 'apps-immich-server', 'running'), NOW + 200);
		expect(m.immich.phase).toBe('running');
	});

	it('failed dominates the aggregation', () => {
		let m = mergeNodeEvent({}, node('immich', 'apps-immich-postgres', 'running'), NOW);
		m = mergeNodeEvent(m, node('immich', 'apps-immich-server', 'failed', 'boom'), NOW + 100);
		expect(m.immich.phase).toBe('failed');
	});

	it('does not duplicate history entries for repeated same-phase events', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'pulling'), NOW);
		m = mergeNodeEvent(m, node('jellyfin', 'apps-jellyfin', 'pulling'), NOW + 100);
		expect(m.jellyfin.phaseHistory).toHaveLength(1);
	});

	it('clears error and percent when the app reaches running', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'failed', 'boom'), NOW);
		m = mergePullEvent(m, { app: 'jellyfin', image: 'x', phase: 'pulling', percent: 50 }, NOW + 100);
		m = mergeNodeEvent(m, node('jellyfin', 'apps-jellyfin', 'running'), NOW + 200);
		expect(m.jellyfin.phase).toBe('running');
		expect(m.jellyfin.error).toBe('');
		expect(m.jellyfin.percent).toBeNull();
	});
});

describe('mergePullEvent', () => {
	it('moves a queued app to pulling with detail and percent', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'queued'), NOW);
		m = mergePullEvent(
			m,
			{ app: 'jellyfin', image: 'jellyfin:10', phase: 'pulling', percent: 34, detail: '34% — 340.0 MiB of 1.0 GiB' },
			NOW + 100
		);
		expect(m.jellyfin.phase).toBe('pulling');
		expect(m.jellyfin.percent).toBe(34);
		expect(m.jellyfin.phaseDetail).toBe('34% — 340.0 MiB of 1.0 GiB');
	});

	it('does not regress a later phase back to pulling', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'configuring'), NOW);
		m = mergePullEvent(m, { app: 'jellyfin', image: 'jellyfin:10', phase: 'pulling', percent: 10 }, NOW + 100);
		expect(m.jellyfin.phase).toBe('configuring');
	});

	it('clears the detail on done so the progress bar hides', () => {
		let m = mergePullEvent(
			{},
			{ app: 'jellyfin', image: 'jellyfin:10', phase: 'pulling', percent: 100, detail: '100% — …' },
			NOW
		);
		m = mergePullEvent(m, { app: 'jellyfin', image: 'jellyfin:10', phase: 'done' }, NOW + 100);
		expect(m.jellyfin.phaseDetail).toBe('');
	});

	it('handles already-local images (no pulling phase seen)', () => {
		const m = mergePullEvent({}, { app: 'navidrome', image: 'navidrome:1', phase: 'done' }, NOW);
		expect(m.navidrome.phase).toBeNull();
		expect(m.navidrome.phaseDetail).toBe('');
	});
});

describe('mergeSnapshot', () => {
	it('drops entries for uninstalled apps and keeps the rest', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'pulling'), NOW);
		m = mergeNodeEvent(m, node('navidrome', 'apps-navidrome', 'configuring'), NOW);
		// Snapshot no longer contains jellyfin (uninstalled).
		m = mergeSnapshot(m, home({ navidrome: 'running' }));
		expect(Object.keys(m)).toEqual(['navidrome']);
		expect(m.navidrome.phase).toBe('configuring');
	});

	it('keeps failed app progress (the modal needs the error)', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'failed', 'boom'), NOW);
		m = mergeSnapshot(m, home({ jellyfin: 'failed' }));
		expect(m.jellyfin.phase).toBe('failed');
	});

	it('settled running apps drop their progress entry', () => {
		let m = mergeNodeEvent({}, node('jellyfin', 'apps-jellyfin', 'running'), NOW);
		m = mergeSnapshot(m, home({ jellyfin: 'running' }));
		expect(m.jellyfin).toBeUndefined();
	});
});

describe('emptyProgress', () => {
	it('starts with no phase and no history', () => {
		const p = emptyProgress(NOW);
		expect(p.phase).toBeNull();
		expect(p.percent).toBeNull();
		expect(p.phaseHistory).toEqual([]);
		expect(p.updatedAt).toBe(NOW);
	});
});
