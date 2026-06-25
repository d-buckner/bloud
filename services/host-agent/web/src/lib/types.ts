// API Response Types

export interface HealthResponse {
	status: string;
}

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
	name: string;
	display_name: string;
	version: string;
	status: AppStatus;
	port?: number;
	is_system: boolean;
	integration_config?: Record<string, string>;
	installed_at: string;
	updated_at: string;
	sso_launch_path?: string;
}

export interface AppsResponse {
	apps: App[];
}

export interface SystemStatus {
	cpu: number;
	memory: number;
	disk: number;
}

export interface ApiError {
	error: string;
}

// Remote app (shared from another host)
export interface RemoteApp {
	id: string;
	host_label: string;
	app_id: string;
	app_name: string;
	sso_strategy: string;
	bypass_paths: string[];
	sidecar_tailnet_addr: string;
	status: string;
	created_at: string;
}

// Catalog types (matches Go catalog.App struct)
export interface CatalogApp {
	name: string;
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

export interface Integration {
	required: boolean;
	multi: boolean;
	compatible: CompatibleApp[];
}

export interface CompatibleApp {
	app: string;
	default?: boolean;
	category?: string;
}

// Intent response (returned by install/uninstall endpoints)
export interface IntentResponse {
	intentId: string;
}

