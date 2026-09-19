// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, it, expect } from 'vitest';
import { widgetRegistry, getWidgetById, isValidWidgetId } from '../registry';

describe('widget registry', () => {
	describe('widgetRegistry', () => {
		it('contains the built-in widgets at their documented default sizes', () => {
			const sizes = Object.fromEntries(
				widgetRegistry.map((w) => [w.id, `${w.size.cols}x${w.size.rows}`])
			);
			expect(sizes).toEqual({
				'system-stats': '2x2',
				storage: '2x2',
				'quick-notes': '2x2',
			});
		});

		it('all widgets have required properties', () => {
			for (const widget of widgetRegistry) {
				expect(widget.id).toBeTruthy();
				expect(widget.name).toBeTruthy();
				expect(widget.description).toBeTruthy();
				expect(widget.icon).toBeTruthy();
				expect(widget.component).toBeDefined();
			}
		});

		it('every widget can grow to at least its default size', () => {
			// A maxSize below the default would make the grid clamp a widget
			// smaller than the registry says it starts at.
			for (const widget of widgetRegistry) {
				expect(widget.maxSize.cols).toBeGreaterThanOrEqual(widget.size.cols);
				expect(widget.maxSize.rows).toBeGreaterThanOrEqual(widget.size.rows);
			}
		});

		it('all widget IDs are unique', () => {
			const ids = widgetRegistry.map((w) => w.id);
			expect(new Set(ids).size).toBe(ids.length);
		});
	});

	describe('getWidgetById', () => {
		it('returns widget definition for valid ID', () => {
			const widget = getWidgetById('system-stats');
			expect(widget).toBeDefined();
			expect(widget?.id).toBe('system-stats');
		});

		it('returns undefined for invalid ID', () => {
			expect(getWidgetById('non-existent-widget')).toBeUndefined();
		});

		it('returns undefined for empty string', () => {
			expect(getWidgetById('')).toBeUndefined();
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
