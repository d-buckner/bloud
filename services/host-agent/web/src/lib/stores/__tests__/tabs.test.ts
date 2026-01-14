import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import { tabs, openAppNames, activeAppName, appPaths } from '../tabs';

describe('tabs store', () => {
	beforeEach(() => {
		tabs.clear();
	});

	describe('open', () => {
		it('adds app to open tabs', () => {
			tabs.open('miniflux');
			expect(get(openAppNames)).toEqual(['miniflux']);
		});

		it('sets app as active', () => {
			tabs.open('miniflux');
			expect(get(activeAppName)).toBe('miniflux');
		});

		it('stores initial path', () => {
			tabs.open('miniflux', 'settings');
			expect(get(appPaths)).toEqual({ miniflux: 'settings' });
		});

		it('defaults to empty path', () => {
			tabs.open('miniflux');
			expect(get(appPaths)).toEqual({ miniflux: '' });
		});

		it('does not duplicate if already open', () => {
			tabs.open('miniflux');
			tabs.open('miniflux');
			expect(get(openAppNames)).toEqual(['miniflux']);
		});

		it('sets existing app as active without duplicating', () => {
			tabs.open('miniflux');
			tabs.open('actual-budget');
			tabs.open('miniflux');
			expect(get(openAppNames)).toEqual(['miniflux', 'actual-budget']);
			expect(get(activeAppName)).toBe('miniflux');
		});

		it('preserves tab order when reopening', () => {
			tabs.open('app1');
			tabs.open('app2');
			tabs.open('app3');
			tabs.open('app1'); // reopen first
			expect(get(openAppNames)).toEqual(['app1', 'app2', 'app3']);
		});
	});

	describe('close', () => {
		it('removes app from open tabs', () => {
			tabs.open('miniflux');
			tabs.open('actual-budget');
			tabs.close('miniflux');
			expect(get(openAppNames)).toEqual(['actual-budget']);
		});

		it('removes path for closed app', () => {
			tabs.open('miniflux', 'settings');
			tabs.close('miniflux');
			expect(get(appPaths)).toEqual({});
		});

		it('returns next active app when closing active', () => {
			tabs.open('app1');
			tabs.open('app2');
			tabs.open('app3');
			const next = tabs.close('app3');
			expect(next).toBe('app2');
		});

		it('sets last tab as active when closing active', () => {
			tabs.open('app1');
			tabs.open('app2');
			tabs.open('app3');
			tabs.close('app3');
			expect(get(activeAppName)).toBe('app2');
		});

		it('returns null when closing last tab', () => {
			tabs.open('miniflux');
			const next = tabs.close('miniflux');
			expect(next).toBeNull();
		});

		it('sets active to null when closing last tab', () => {
			tabs.open('miniflux');
			tabs.close('miniflux');
			expect(get(activeAppName)).toBeNull();
		});

		it('keeps current active when closing non-active tab', () => {
			tabs.open('app1');
			tabs.open('app2');
			tabs.open('app3');
			tabs.close('app1');
			expect(get(activeAppName)).toBe('app3');
		});

		it('returns current active when closing non-active tab', () => {
			tabs.open('app1');
			tabs.open('app2');
			tabs.open('app3');
			const next = tabs.close('app1');
			expect(next).toBe('app3');
		});
	});

	describe('setActive', () => {
		it('sets active app', () => {
			tabs.open('app1');
			tabs.open('app2');
			tabs.setActive('app1');
			expect(get(activeAppName)).toBe('app1');
		});

		it('can set to null', () => {
			tabs.open('app1');
			tabs.setActive(null);
			expect(get(activeAppName)).toBeNull();
		});
	});

	describe('setPath', () => {
		it('updates path for app', () => {
			tabs.open('miniflux');
			tabs.setPath('miniflux', 'entries/unread');
			expect(get(appPaths)).toEqual({ miniflux: 'entries/unread' });
		});

		it('preserves other app paths', () => {
			tabs.open('app1', 'path1');
			tabs.open('app2', 'path2');
			tabs.setPath('app1', 'new-path');
			expect(get(appPaths)).toEqual({ app1: 'new-path', app2: 'path2' });
		});
	});

	describe('getPath', () => {
		it('returns path for app', () => {
			tabs.open('miniflux', 'settings');
			expect(tabs.getPath('miniflux')).toBe('settings');
		});

		it('returns empty string for unknown app', () => {
			expect(tabs.getPath('unknown')).toBe('');
		});
	});

	describe('isOpen', () => {
		it('returns true for open app', () => {
			tabs.open('miniflux');
			expect(tabs.isOpen('miniflux')).toBe(true);
		});

		it('returns false for closed app', () => {
			expect(tabs.isOpen('miniflux')).toBe(false);
		});

		it('returns false after closing', () => {
			tabs.open('miniflux');
			tabs.close('miniflux');
			expect(tabs.isOpen('miniflux')).toBe(false);
		});
	});

	describe('clear', () => {
		it('removes all tabs', () => {
			tabs.open('app1');
			tabs.open('app2');
			tabs.clear();
			expect(get(openAppNames)).toEqual([]);
		});

		it('clears active', () => {
			tabs.open('app1');
			tabs.clear();
			expect(get(activeAppName)).toBeNull();
		});

		it('clears all paths', () => {
			tabs.open('app1', 'path1');
			tabs.open('app2', 'path2');
			tabs.clear();
			expect(get(appPaths)).toEqual({});
		});
	});
});
