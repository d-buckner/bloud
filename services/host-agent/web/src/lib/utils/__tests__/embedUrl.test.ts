import { describe, it, expect } from 'vitest';
import { getEmbedUrl, extractRelativePath, getAppRouteUrl } from '../embedUrl';

describe('getEmbedUrl', () => {
	it('returns base path for empty path', () => {
		expect(getEmbedUrl('miniflux', '')).toBe('/embed/miniflux/');
	});

	it('returns base path when path is undefined', () => {
		expect(getEmbedUrl('miniflux')).toBe('/embed/miniflux/');
	});

	it('appends path to base', () => {
		expect(getEmbedUrl('miniflux', 'settings')).toBe('/embed/miniflux/settings');
	});

	it('handles nested paths', () => {
		expect(getEmbedUrl('miniflux', 'entries/unread')).toBe('/embed/miniflux/entries/unread');
	});

	it('strips leading slash from path', () => {
		expect(getEmbedUrl('miniflux', '/settings')).toBe('/embed/miniflux/settings');
	});

	it('handles paths with query strings', () => {
		expect(getEmbedUrl('miniflux', 'search?q=test')).toBe('/embed/miniflux/search?q=test');
	});

	it('handles app names with hyphens', () => {
		expect(getEmbedUrl('actual-budget', 'budget')).toBe('/embed/actual-budget/budget');
	});
});

describe('extractRelativePath', () => {
	it('extracts path from embed URL', () => {
		expect(extractRelativePath('/embed/miniflux/settings', 'miniflux')).toBe('settings');
	});

	it('extracts nested path', () => {
		expect(extractRelativePath('/embed/miniflux/entries/unread', 'miniflux')).toBe(
			'entries/unread'
		);
	});

	it('returns empty string for base path', () => {
		expect(extractRelativePath('/embed/miniflux/', 'miniflux')).toBe('');
	});

	it('returns null for non-matching app', () => {
		expect(extractRelativePath('/embed/miniflux/settings', 'actual-budget')).toBeNull();
	});

	it('returns null for non-embed path', () => {
		expect(extractRelativePath('/apps/miniflux/settings', 'miniflux')).toBeNull();
	});

	it('returns null for partial match', () => {
		expect(extractRelativePath('/embed/mini', 'miniflux')).toBeNull();
	});

	it('handles app names with hyphens', () => {
		expect(extractRelativePath('/embed/actual-budget/accounts', 'actual-budget')).toBe('accounts');
	});
});

describe('getAppRouteUrl', () => {
	it('returns base path for empty path', () => {
		expect(getAppRouteUrl('miniflux', '')).toBe('/apps/miniflux/');
	});

	it('returns base path when path is undefined', () => {
		expect(getAppRouteUrl('miniflux')).toBe('/apps/miniflux/');
	});

	it('appends path to base', () => {
		expect(getAppRouteUrl('miniflux', 'settings')).toBe('/apps/miniflux/settings');
	});

	it('handles nested paths', () => {
		expect(getAppRouteUrl('miniflux', 'entries/unread')).toBe('/apps/miniflux/entries/unread');
	});

	it('strips leading slash from path', () => {
		expect(getAppRouteUrl('miniflux', '/settings')).toBe('/apps/miniflux/settings');
	});

	it('handles paths with query strings', () => {
		expect(getAppRouteUrl('miniflux', 'search?q=test')).toBe('/apps/miniflux/search?q=test');
	});
});
