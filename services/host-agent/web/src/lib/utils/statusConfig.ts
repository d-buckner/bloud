/**
 * Status configuration for app frame display
 *
 * Pure functions to determine what status UI to show based on app state.
 */

import { AppStatus, type App } from '$lib/types';

export interface StatusIcon {
	name: string;
	class: string;
}

export interface StatusConfig {
	spinner?: 'small' | 'large';
	icon?: StatusIcon;
	title: string;
	titleClass?: string;
	subtitle?: string;
}

export interface AppFrameState {
	loading: boolean;
	error: string | null;
	bootstrapped: boolean;
}

/**
 * Determine if an app is ready to show its iframe
 */
export function isAppReady(app: App, state: AppFrameState): boolean {
	return state.bootstrapped && !state.error && app.status === AppStatus.Running && !!app.port;
}

/**
 * Get status display configuration for an app frame
 * Returns null if app is ready (should show iframe instead)
 */
export function getStatusConfig(app: App, state: AppFrameState): StatusConfig | null {
	if (isAppReady(app, state)) return null;

	if (state.loading) {
		return { spinner: 'small', title: `Loading ${app.display_name}...` };
	}

	if (state.error) {
		return {
			icon: { name: 'warning', class: 'error' },
			title: state.error,
			titleClass: 'error'
		};
	}

	if (app.status === AppStatus.Installing) {
		return {
			spinner: 'large',
			title: `Installing ${app.display_name}`,
			subtitle: 'This may take a few minutes...'
		};
	}

	if (app.status === AppStatus.Starting) {
		return {
			spinner: 'large',
			title: `Starting ${app.display_name}`,
			subtitle: 'Waiting for health check...'
		};
	}

	if (app.status === AppStatus.Stopped) {
		return {
			icon: { name: 'stop', class: 'stopped' },
			title: `${app.display_name} is stopped`
		};
	}

	if (app.status === AppStatus.Error || app.status === AppStatus.Failed) {
		return {
			icon: { name: 'warning', class: 'error' },
			title: `${app.display_name} failed to start`,
			subtitle: 'Check logs for more details'
		};
	}

	return {
		title: 'App is not available',
		subtitle: `Status: ${app.status}`
	};
}
