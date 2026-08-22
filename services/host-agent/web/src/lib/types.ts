// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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
	integration_config?: Record<string, string>;
	installed_at: string;
	updated_at: string;
	sso_launch_path?: string;
}

// Remote app (shared from another host)
export interface RemoteApp {
	id: string;
	host_label: string;
	app_id: string;
	app_name: string;
	sso_strategy: string;
	bypass_paths: string[];
	tailnet_addr: string;
	status: string;
	created_at: string;
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

// Guest (contact book entry)
export interface Guest {
	id: string;
	name: string;
	created_at: string;
}

// Invite token payload (HMAC-SHA256 signed, base64url-encoded JSON)
export interface InvitePayload {
	appId: string;
	appName: string;
	hostLabel: string;
	tailnetAddr: string;
	nodeShareLink: string;
	exp: number;
}

// Intent response (returned by install/uninstall endpoints).
// Install 202s additionally carry the current app record (the orchestrator
// records the installing row at submit time) so the UI can render the tile
// immediately.
export interface IntentResponse {
	intentId: string;
	app?: App;
}

// Grid element — null x/y means autoPosition (GridStack picks the cell)
export interface GridElement {
	type: 'app' | 'widget';
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

export interface HomeData {
	apps: HomeApp[];
	widgets: HomeWidget[];
}

