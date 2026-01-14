import { describe, it, expect } from 'vitest';
import { getStatusConfig, isAppReady, type AppFrameState } from '../statusConfig';
import { AppStatus, type App } from '$lib/types';

function createApp(overrides: Partial<App> = {}): App {
	return {
		id: 1,
		name: 'test-app',
		display_name: 'Test App',
		version: '1.0.0',
		status: AppStatus.Running,
		port: 8080,
		is_system: false,
		installed_at: '2024-01-01T00:00:00Z',
		updated_at: '2024-01-01T00:00:00Z',
		...overrides
	};
}

function createState(overrides: Partial<AppFrameState> = {}): AppFrameState {
	return {
		loading: false,
		error: null,
		bootstrapped: true,
		...overrides
	};
}

describe('isAppReady', () => {
	it('returns true when bootstrapped, no error, running, and has port', () => {
		const app = createApp();
		const state = createState();
		expect(isAppReady(app, state)).toBe(true);
	});

	it('returns false when loading', () => {
		const app = createApp();
		const state = createState({ loading: true, bootstrapped: false });
		expect(isAppReady(app, state)).toBe(false);
	});

	it('returns false when has error', () => {
		const app = createApp();
		const state = createState({ error: 'Something went wrong' });
		expect(isAppReady(app, state)).toBe(false);
	});

	it('returns false when not bootstrapped', () => {
		const app = createApp();
		const state = createState({ bootstrapped: false });
		expect(isAppReady(app, state)).toBe(false);
	});

	it('returns false when not running', () => {
		const app = createApp({ status: AppStatus.Starting });
		const state = createState();
		expect(isAppReady(app, state)).toBe(false);
	});

	it('returns false when no port', () => {
		const app = createApp({ port: undefined });
		const state = createState();
		expect(isAppReady(app, state)).toBe(false);
	});
});

describe('getStatusConfig', () => {
	it('returns null when app is ready', () => {
		const app = createApp();
		const state = createState();
		expect(getStatusConfig(app, state)).toBeNull();
	});

	describe('loading state', () => {
		it('returns spinner and loading message', () => {
			const app = createApp();
			const state = createState({ loading: true, bootstrapped: false });
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				spinner: 'small',
				title: 'Loading Test App...'
			});
		});
	});

	describe('error state', () => {
		it('returns error icon and message', () => {
			const app = createApp();
			const state = createState({ error: 'Bootstrap failed' });
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				icon: { name: 'warning', class: 'error' },
				title: 'Bootstrap failed',
				titleClass: 'error'
			});
		});
	});

	describe('installing state', () => {
		it('returns large spinner and installing message', () => {
			const app = createApp({ status: AppStatus.Installing });
			const state = createState({ bootstrapped: false });
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				spinner: 'large',
				title: 'Installing Test App',
				subtitle: 'This may take a few minutes...'
			});
		});
	});

	describe('starting state', () => {
		it('returns large spinner and starting message', () => {
			const app = createApp({ status: AppStatus.Starting });
			const state = createState();
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				spinner: 'large',
				title: 'Starting Test App',
				subtitle: 'Waiting for health check...'
			});
		});
	});

	describe('stopped state', () => {
		it('returns stop icon and stopped message', () => {
			const app = createApp({ status: AppStatus.Stopped });
			const state = createState();
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				icon: { name: 'stop', class: 'stopped' },
				title: 'Test App is stopped'
			});
		});
	});

	describe('error/failed status', () => {
		it('returns error icon for Error status', () => {
			const app = createApp({ status: AppStatus.Error });
			const state = createState();
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				icon: { name: 'warning', class: 'error' },
				title: 'Test App failed to start',
				subtitle: 'Check logs for more details'
			});
		});

		it('returns error icon for Failed status', () => {
			const app = createApp({ status: AppStatus.Failed });
			const state = createState();
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				icon: { name: 'warning', class: 'error' },
				title: 'Test App failed to start',
				subtitle: 'Check logs for more details'
			});
		});
	});

	describe('unavailable state', () => {
		it('returns unavailable message for unknown status', () => {
			const app = createApp({ status: 'unknown' as AppStatus });
			const state = createState();
			const config = getStatusConfig(app, state);

			expect(config).toEqual({
				title: 'App is not available',
				subtitle: 'Status: unknown'
			});
		});
	});
});
