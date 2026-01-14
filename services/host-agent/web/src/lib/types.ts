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
	bootstrap?: BootstrapConfig;
}

// Bootstrap configuration for client-side app pre-configuration
export interface BootstrapConfig {
	indexedDB?: IndexedDBConfig;
}

export interface IndexedDBConfig {
	database: string;
	// Intercepts: Values returned on read, regardless of stored value
	// Injected into iframe context via service worker
	intercepts?: IndexedDBEntry[];
	// Writes: Values written from main page before iframe loads
	// Use for values that apps don't overwrite on init
	writes?: IndexedDBEntry[];
	// Legacy: entries field (deprecated, use intercepts/writes instead)
	entries?: IndexedDBEntry[];
}

export interface IndexedDBEntry {
	store: string;
	key: string;
	value: string;
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

// Install plan types
export interface InstallPlan {
	app: string;
	canInstall: boolean;
	blockers: string[];
	choices: IntegrationChoice[];
	autoConfig: ConfigTask[];
	dependents: ConfigTask[];
}

export interface IntegrationChoice {
	integration: string;
	required: boolean;
	installed: ChoiceOption[];
	available: ChoiceOption[];
	recommended: string;
}

export interface ChoiceOption {
	app: string;
	default: boolean;
	category?: string;
}

export interface ConfigTask {
	target: string;
	source: string;
	integration: string;
}

// Install result (Nix-based)
export interface InstallResult {
	app: string;
	success: boolean;
	error?: string;
	appsInstalled?: string[];
	configured?: string[];
	configErrors?: string[];
	rebuildOutput?: string;
	generationInfo?: string;
}

// Remove plan types
export interface RemovePlan {
	app: string;
	canRemove: boolean;
	blockers: string[];
	willUnconfigure: string[];
}

// Uninstall result
export interface UninstallResult {
	app: string;
	success: boolean;
	error?: string;
	unconfigured?: string[];
}

// Rollback result
export interface RollbackResult {
	success: boolean;
	output: string;
	errorMessage?: string;
	changes?: string[];
	duration: string;
}

// NixOS Generation
export interface Generation {
	number: number;
	date: string;
	current: boolean;
	nixosVersion?: string;
	description?: string;
}

export interface GenerationsResponse {
	generations: Generation[];
}
