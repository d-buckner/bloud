import type { Component } from 'svelte';
import ServiceHealth from './ServiceHealth.svelte';
import Storage from './Storage.svelte';
import Weather from './Weather.svelte';

/**
 * Widget size in grid units
 * Grid is 2 columns, rows are fixed height
 */
export interface WidgetSize {
	/** Width in columns (1 = half, 2 = full) */
	cols: 1 | 2;
	/** Height in rows */
	rows: 1 | 2 | 3;
}

/**
 * Props that all widget components receive
 */
export interface WidgetProps {
	/** Widget-specific configuration */
	config: Record<string, unknown>;
	/** Callback to open configuration modal (if configurable) */
	onConfigure?: () => void;
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
	component: Component<WidgetProps>;
	/** Default configuration for the widget */
	defaultConfig: Record<string, unknown>;
	/** Widget size in grid units (cols x rows) */
	size: WidgetSize;
	/** Whether the widget has a configuration modal */
	configurable: boolean;
}

/**
 * Registry of all available widgets
 */
export const widgetRegistry: WidgetDefinition[] = [
	{
		id: 'service-health',
		name: 'Service Health',
		description: 'Shows the status of your installed apps',
		component: ServiceHealth,
		defaultConfig: {},
		size: { cols: 2, rows: 1 },
		configurable: false,
	},
	{
		id: 'storage',
		name: 'Storage',
		description: 'Shows disk usage and available space',
		component: Storage,
		defaultConfig: {},
		size: { cols: 1, rows: 1 },
		configurable: false,
	},
	{
		id: 'weather',
		name: 'Weather',
		description: 'Current weather and forecast for your location',
		component: Weather,
		defaultConfig: {
			latitude: 40.7128,
			longitude: -74.006,
			locationName: 'New York, NY',
		},
		size: { cols: 1, rows: 1 },
		configurable: true,
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
