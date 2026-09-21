// SPDX-License-Identifier: AGPL-3.0-only
import type { Component } from 'svelte';
import SystemStats from './SystemStats.svelte';
import Storage from './Storage.svelte';
import QuickNotes from './QuickNotes.svelte';

/**
 * Widget size in grid units. The dashboard grid is 6 columns wide and rows
 * are a fixed height, so `cols: 2, rows: 2` is roughly 360x260 at desktop
 * width.
 */
export interface WidgetSize {
	/** Width in columns (1-6 in a 6-col grid). */
	cols: number;
	/** Height in rows. */
	rows: number;
}

/**
 * Definition for a widget that can be displayed on the home page.
 */
export interface WidgetDefinition {
	/** Unique identifier for the widget. */
	id: string;
	/** Display name shown in the widget header and picker. */
	name: string;
	/** One-line description shown in the widget picker. */
	description: string;
	/** Icon name under `static/icons` used by the picker. */
	icon: string;
	/** The Svelte component to render. */
	component: Component;
	/** Size the widget starts at, and its minimum when resized. */
	size: WidgetSize;
	/** Largest size the widget can be resized to. */
	maxSize: WidgetSize;
}

/**
 * Registry of all available widgets. Sizes are grid-unit defaults, not
 * ceilings: the user can drag a widget larger up to `maxSize`.
 */
export const widgetRegistry: WidgetDefinition[] = [
	{
		id: 'system-stats',
		name: 'System',
		description: 'Live CPU, memory, and disk load',
		icon: 'activity',
		component: SystemStats,
		size: { cols: 2, rows: 2 },
		maxSize: { cols: 4, rows: 3 },
	},
	{
		id: 'storage',
		name: 'Storage',
		description: 'Disk capacity and free space',
		icon: 'hard-drive',
		component: Storage,
		size: { cols: 2, rows: 2 },
		maxSize: { cols: 4, rows: 3 },
	},
	{
		id: 'quick-notes',
		name: 'Notes',
		description: 'A scratchpad that stays in this browser',
		icon: 'note',
		component: QuickNotes,
		size: { cols: 2, rows: 2 },
		maxSize: { cols: 4, rows: 4 },
	},
];

/**
 * Get a widget definition by ID
 */
export function getWidgetById(id: string): WidgetDefinition | undefined {
	return widgetRegistry.find((w) => w.id === id);
}

/**
 * Check if a widget ID is valid
 */
export function isValidWidgetId(id: string): boolean {
	return widgetRegistry.some((w) => w.id === id);
}
