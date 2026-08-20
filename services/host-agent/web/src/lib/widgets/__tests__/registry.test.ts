// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, it, expect } from 'vitest';
import { widgetRegistry, getWidgetById, isValidWidgetId } from '../registry';

describe('widget registry', () => {
	describe('widgetRegistry', () => {
		it('contains system-stats widget', () => {
			const widget = widgetRegistry.find((w) => w.id === 'system-stats');
			expect(widget).toBeDefined();
			expect(widget?.name).toBe('System');
			expect(widget?.size).toEqual({ cols: 2, rows: 3 });
		});

		it('contains storage widget', () => {
			const widget = widgetRegistry.find((w) => w.id === 'storage');
			expect(widget).toBeDefined();
			expect(widget?.name).toBe('Storage');
			expect(widget?.size).toEqual({ cols: 2, rows: 2 });
		});

		it('contains quick-notes widget', () => {
			const widget = widgetRegistry.find((w) => w.id === 'quick-notes');
			expect(widget).toBeDefined();
			expect(widget?.name).toBe('Notes');
			expect(widget?.size).toEqual({ cols: 2, rows: 2 });
		});

		it('all widgets have required properties', () => {
			for (const widget of widgetRegistry) {
				expect(widget.id).toBeTruthy();
				expect(widget.name).toBeTruthy();
				expect(widget.description).toBeTruthy();
				expect(widget.component).toBeDefined();
				expect(widget.size).toHaveProperty('cols');
				expect(widget.size).toHaveProperty('rows');
				expect([1, 2, 3]).toContain(widget.size.cols);
				expect([1, 2, 3]).toContain(widget.size.rows);
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
			const widget = getWidgetById('system-stats');
			expect(widget).toBeDefined();
			expect(widget?.id).toBe('system-stats');
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

	describe('isValidWidgetId', () => {
		it('returns true for valid widget ID', () => {
			expect(isValidWidgetId('system-stats')).toBe(true);
		});

		it('returns false for invalid widget ID', () => {
			expect(isValidWidgetId('non-existent')).toBe(false);
		});

		it('returns false for empty string', () => {
			expect(isValidWidgetId('')).toBe(false);
		});
	});
});
