import { describe, it, expect } from 'vitest';
import {
	widgetRegistry,
	getWidgetById,
	getAllWidgetIds,
	isValidWidgetId,
} from '../registry';

describe('widget registry', () => {
	describe('widgetRegistry', () => {
		it('contains service-health widget', () => {
			const widget = widgetRegistry.find((w) => w.id === 'service-health');
			expect(widget).toBeDefined();
			expect(widget?.name).toBe('Service Health');
		});

		it('contains storage widget', () => {
			const widget = widgetRegistry.find((w) => w.id === 'storage');
			expect(widget).toBeDefined();
			expect(widget?.name).toBe('Storage');
			expect(widget?.size).toEqual({ cols: 1, rows: 1 });
		});

		it('contains weather widget', () => {
			const widget = widgetRegistry.find((w) => w.id === 'weather');
			expect(widget).toBeDefined();
			expect(widget?.name).toBe('Weather');
			expect(widget?.size).toEqual({ cols: 1, rows: 1 });
			expect(widget?.configurable).toBe(true);
			expect(widget?.defaultConfig).toHaveProperty('latitude');
			expect(widget?.defaultConfig).toHaveProperty('longitude');
			expect(widget?.defaultConfig).toHaveProperty('locationName');
		});

		it('all widgets have required properties', () => {
			for (const widget of widgetRegistry) {
				expect(widget.id).toBeTruthy();
				expect(widget.name).toBeTruthy();
				expect(widget.description).toBeTruthy();
				expect(widget.component).toBeDefined();
				expect(widget.defaultConfig).toBeDefined();
				expect(widget.size).toHaveProperty('cols');
				expect(widget.size).toHaveProperty('rows');
				expect([1, 2]).toContain(widget.size.cols);
				expect([1, 2, 3]).toContain(widget.size.rows);
				expect(typeof widget.configurable).toBe('boolean');
			}
		});

		it('all widget IDs are unique', () => {
			const ids = widgetRegistry.map((w) => w.id);
			const uniqueIds = new Set(ids);
			expect(uniqueIds.size).toBe(ids.length);
		});
	});

	describe('getWidgetById', () => {
		it('returns widget definition for valid ID', () => {
			const widget = getWidgetById('service-health');
			expect(widget).toBeDefined();
			expect(widget?.id).toBe('service-health');
		});

		it('returns undefined for invalid ID', () => {
			const widget = getWidgetById('non-existent-widget');
			expect(widget).toBeUndefined();
		});

		it('returns undefined for empty string', () => {
			const widget = getWidgetById('');
			expect(widget).toBeUndefined();
		});
	});

	describe('getAllWidgetIds', () => {
		it('returns array of widget IDs', () => {
			const ids = getAllWidgetIds();
			expect(Array.isArray(ids)).toBe(true);
			expect(ids.length).toBeGreaterThan(0);
		});

		it('includes service-health', () => {
			const ids = getAllWidgetIds();
			expect(ids).toContain('service-health');
		});

		it('includes storage', () => {
			const ids = getAllWidgetIds();
			expect(ids).toContain('storage');
		});

		it('includes weather', () => {
			const ids = getAllWidgetIds();
			expect(ids).toContain('weather');
		});
	});

	describe('isValidWidgetId', () => {
		it('returns true for valid widget ID', () => {
			expect(isValidWidgetId('service-health')).toBe(true);
		});

		it('returns false for invalid widget ID', () => {
			expect(isValidWidgetId('non-existent')).toBe(false);
		});

		it('returns false for empty string', () => {
			expect(isValidWidgetId('')).toBe(false);
		});
	});
});
