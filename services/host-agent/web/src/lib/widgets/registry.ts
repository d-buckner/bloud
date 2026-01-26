import type { Component } from 'svelte';
import SystemStats from './SystemStats.svelte';
import Storage from './Storage.svelte';
import QuickNotes from './QuickNotes.svelte';

/**
 * Widget size in grid units
 * Grid is 6 columns, rows are fixed height
 */
export interface WidgetSize {
	/** Width in columns (1-3 in a 6-col grid) */
	cols: 1 | 2 | 3;
	/** Height in rows */
	rows: 1 | 2 | 3;
}

/**
 * Definition for a widget that can be displayed on the home page
 */
export interface WidgetDefinition {
	/** Unique identifier for the widget */
	id: string;
	/** Display name shown in the widget header */
	name: string;
	/** Description shown in the widget picker */
	description: string;
	/** The Svelte component to render */
	component: Component;
	/** Widget size in grid units (cols x rows) */
	size: WidgetSize;
}

/**
 * Registry of all available widgets
 */
export const widgetRegistry: WidgetDefinition[] = [
	{
		id: 'system-stats',
		name: 'System',
		description: 'CPU, memory, and disk usage',
		component: SystemStats,
		size: { cols: 2, rows: 3 },
	},
	{
		id: 'storage',
		name: 'Storage',
		description: 'Disk space breakdown',
		component: Storage,
		size: { cols: 2, rows: 2 },
	},
	{
		id: 'quick-notes',
		name: 'Notes',
		description: 'Quick notes and reminders',
		component: QuickNotes,
		size: { cols: 2, rows: 2 },
	},
];

/**
 * Get a widget definition by ID
 */
export function getWidgetById(id: string): WidgetDefinition | undefined {
	return widgetRegistry.find((w) => w.id === id);
}

/**
 * Get all widget IDs
 */
export function getAllWidgetIds(): string[] {
	return widgetRegistry.map((w) => w.id);
}

/**
 * Check if a widget ID is valid
 */
export function isValidWidgetId(id: string): boolean {
	return widgetRegistry.some((w) => w.id === id);
}
