// SPDX-License-Identifier: AGPL-3.0-only
// API Response Types

export type AppStatus = 'running' | 'starting' | 'installing' | 'uninstalling' | 'stopped' | 'error' | 'failed';

export const AppStatus = {
	Running: 'running',
	Starting: 'starting',
	Installing: 'installing',
	Uninstalling: 'uninstalling',
	Stopped: 'stopped',
	Error: 'error',
	Failed: 'failed'
} as const;

export interface App {
	id: number;
	catalog_id: string;
	display_name: string;
	version: string;
	status: AppStatus;
	/** Persisted failure reason (cleared on successful re-install). */
	last_error?: string;
	port?: number;
	is_system: boolean;
	/**
	 * Catalog-derived: the app has no browser UI of its own. The home grid
	 * leaves such an app out, because a tile is an invitation to open a page
	 * that does not exist. The app is otherwise ordinary, and it stays in the
	 * `apps` store so its status still drives toasts and install progress.
	 */
	headless?: boolean;
	/** Catalog-derived: the app publishes a credential for a human-held client. */
	has_client_access?: boolean;
	integration_config?: Record<string, string>;
	installed_at: string;
	updated_at: string;
	sso_launch_path?: string;
}

// Catalog types (matches Go catalog.App struct)
export interface CatalogApp {
	catalogId: string;
	displayName: string;
	description: string;
	category: string;
	icon?: string;
	screenshots?: string[];
	version?: string;
	port?: number;
	isSystem?: boolean;
	dependencies?: string[];
	resources?: Resources;
	sso?: SSO;
	defaultConfig?: Record<string, unknown>;
	healthCheck?: HealthCheck;
	docs?: Docs;
	tags?: string[];
	// Approximate total image download size, for pull expectations. Absent
	// when unknown (no declared estimate and no local images to measure).
	estimatedSizeMB?: number;
}

export interface Resources {
	minRam?: number;
	minCpu?: number;
	minDisk?: number;
}

export interface SSO {
	enabled?: boolean;
	provider?: string;
}

export interface HealthCheck {
	path?: string;
	interval?: number;
}

export interface Docs {
	url?: string;
	setup?: string;
}

// Intent response (returned by install/uninstall endpoints).
// Install 202s additionally carry the current app record (the orchestrator
// records the installing row at submit time) so the UI can render the tile
// immediately.
export interface IntentResponse {
	intentId: string;
	app?: App;
}

// Grid element: null x/y means autoPosition (GridStack picks the cell)
export interface GridElement {
	type: 'app' | 'widget' | 'launcher';
	id: string;
	x: number | null;
	y: number | null;
	w: number;
	h: number;
}

// Home endpoint response shapes
export interface HomeApp extends App {
	x: number | null;
	y: number | null;
	w: number;
	h: number;
}

export interface HomeWidget {
	id: string;
	x: number | null;
	y: number | null;
	w: number;
	h: number;
}

// Launcher: an operator-declared tile that opens a URL and wires to nothing.
export interface Launcher {
	id: string;
	name: string;
	url: string;
	icon: string;
	/** Catalog ID a remote install stands in for; lets the tile borrow that app's icon. */
	app?: string;
	x: number | null;
	y: number | null;
	w: number;
	h: number;
}

export interface HomeData {
	apps: HomeApp[];
	/** Optional so older snapshots (and tests) that predate launchers still typecheck. */
	launchers?: Launcher[];
	widgets: HomeWidget[];
}

