import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
	loadPrefs,
	savePrefs,
	DEFAULT_PREFS,
	type WidgetPrefs,
} from '../widgetPrefs';

// Mock $app/environment
vi.mock('$app/environment', () => ({
	browser: true,
}));

// Mock localStorage
const mockStorage = new Map<string, string>();
const mockLocalStorage = {
	getItem: vi.fn((key: string) => mockStorage.get(key) ?? null),
	setItem: vi.fn((key: string, value: string) => mockStorage.set(key, value)),
	removeItem: vi.fn((key: string) => mockStorage.delete(key)),
	clear: vi.fn(() => mockStorage.clear()),
};

vi.stubGlobal('localStorage', mockLocalStorage);

describe('widgetPrefs', () => {
	beforeEach(() => {
		mockStorage.clear();
		vi.clearAllMocks();
	});

	describe('DEFAULT_PREFS', () => {
		it('has service-health enabled by default', () => {
			expect(DEFAULT_PREFS.enabled).toContain('service-health');
		});

		it('has empty configs by default', () => {
			expect(DEFAULT_PREFS.configs).toEqual({});
		});
	});

	describe('loadPrefs', () => {
		it('returns default prefs when localStorage is empty', () => {
			const prefs = loadPrefs();
			expect(prefs).toEqual(DEFAULT_PREFS);
		});

		it('loads valid prefs from localStorage', () => {
			const stored: WidgetPrefs = {
				enabled: ['service-health'],
				configs: { 'service-health': { foo: 'bar' } },
			};
			mockStorage.set('bloud-widget-prefs', JSON.stringify(stored));

			const prefs = loadPrefs();
			expect(prefs.enabled).toEqual(['service-health']);
			expect(prefs.configs).toEqual({ 'service-health': { foo: 'bar' } });
		});

		it('filters out invalid widget IDs', () => {
			const stored: WidgetPrefs = {
				enabled: ['service-health', 'invalid-widget', 'another-invalid'],
				configs: {},
			};
			mockStorage.set('bloud-widget-prefs', JSON.stringify(stored));

			const prefs = loadPrefs();
			expect(prefs.enabled).toEqual(['service-health']);
		});

		it('handles malformed JSON gracefully', () => {
			mockStorage.set('bloud-widget-prefs', 'not valid json');

			const prefs = loadPrefs();
			expect(prefs).toEqual(DEFAULT_PREFS);
		});

		it('handles missing enabled array gracefully', () => {
			mockStorage.set('bloud-widget-prefs', JSON.stringify({ configs: {} }));

			const prefs = loadPrefs();
			expect(prefs.enabled).toEqual(DEFAULT_PREFS.enabled);
		});
	});

	describe('savePrefs', () => {
		it('saves prefs to localStorage', () => {
			const prefs: WidgetPrefs = {
				enabled: ['service-health'],
				configs: { 'service-health': { test: true } },
			};

			savePrefs(prefs);

			expect(mockLocalStorage.setItem).toHaveBeenCalledWith(
				'bloud-widget-prefs',
				JSON.stringify(prefs)
			);
		});
	});
});
