import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { connectLogs } from '../logs';

interface MockEventSource {
	url: string;
	onopen: ((event: Event) => void) | null;
	onmessage: ((event: MessageEvent) => void) | null;
	onerror: ((event: Event) => void) | null;
	readyState: number;
	close: ReturnType<typeof vi.fn>;
}

const READY_STATE_OPEN = 1;
const READY_STATE_CLOSED = 2;

let mockEventSource: MockEventSource;
let instances: MockEventSource[] = [];

class MockEventSourceClass {
	url: string;
	onopen: ((event: Event) => void) | null = null;
	onmessage: ((event: MessageEvent) => void) | null = null;
	onerror: ((event: Event) => void) | null = null;
	readyState = READY_STATE_OPEN;
	close = vi.fn(() => {
		this.readyState = READY_STATE_CLOSED;
	});

	constructor(url: string) {
		this.url = url;
		mockEventSource = this;
		instances.push(this);
	}

	static readonly CONNECTING = 0;
	static readonly OPEN = 1;
	static readonly CLOSED = 2;
}

describe('logs API', () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.stubGlobal('EventSource', MockEventSourceClass);
		instances = [];
	});

	afterEach(() => {
		vi.useRealTimers();
		vi.unstubAllGlobals();
	});

	describe('connectLogs', () => {
		it('connects to correct endpoint', () => {
			connectLogs('my-app', vi.fn());
			expect(mockEventSource.url).toBe('/api/apps/my-app/logs');
		});

		it('starts with empty state', () => {
			const connection = connectLogs('my-app', vi.fn());
			expect(connection.logs).toEqual([]);
			expect(connection.connected).toBe(false);
			expect(connection.error).toBeNull();
		});

		it('sets connected on open', () => {
			const onUpdate = vi.fn();
			const connection = connectLogs('my-app', onUpdate);

			mockEventSource.onopen?.(new Event('open'));

			expect(connection.connected).toBe(true);
			expect(onUpdate).toHaveBeenCalled();
		});

		it('adds logs on message', () => {
			const onUpdate = vi.fn();
			const connection = connectLogs('my-app', onUpdate);

			mockEventSource.onmessage?.({ data: 'log line 1' } as MessageEvent);
			mockEventSource.onmessage?.({ data: 'log line 2' } as MessageEvent);

			expect(connection.logs).toEqual(['log line 1', 'log line 2']);
			expect(onUpdate).toHaveBeenCalledTimes(2);
		});

		it('trims logs when exceeding max', () => {
			const connection = connectLogs('my-app', vi.fn());

			for (let i = 0; i < 1001; i++) {
				mockEventSource.onmessage?.({ data: `line ${i}` } as MessageEvent);
			}

			expect(connection.logs.length).toBe(500);
			expect(connection.logs[0]).toBe('line 501');
			expect(connection.logs[499]).toBe('line 1000');
		});

		it('sets error on connection close', () => {
			const onUpdate = vi.fn();
			const connection = connectLogs('my-app', onUpdate);

			mockEventSource.readyState = READY_STATE_CLOSED;
			mockEventSource.onerror?.(new Event('error'));

			expect(connection.error).toBe('Connection closed');
			expect(connection.connected).toBe(false);
			expect(onUpdate).toHaveBeenCalled();
		});

		it('reconnects after error', () => {
			connectLogs('my-app', vi.fn());

			mockEventSource.readyState = READY_STATE_CLOSED;
			mockEventSource.onerror?.(new Event('error'));

			expect(instances.length).toBe(1);

			vi.advanceTimersByTime(3000);

			expect(instances.length).toBe(2);
			expect(instances[1].url).toBe('/api/apps/my-app/logs');
		});

		it('clears error on reconnect', () => {
			const onUpdate = vi.fn();
			const connection = connectLogs('my-app', onUpdate);

			mockEventSource.readyState = READY_STATE_CLOSED;
			mockEventSource.onerror?.(new Event('error'));
			expect(connection.error).toBe('Connection closed');

			vi.advanceTimersByTime(3000);
			mockEventSource.onopen?.(new Event('open'));

			expect(connection.error).toBeNull();
			expect(connection.connected).toBe(true);
		});
	});

	describe('disconnect', () => {
		it('closes EventSource', () => {
			const connection = connectLogs('my-app', vi.fn());
			const closeFn = mockEventSource.close;

			connection.disconnect();

			expect(closeFn).toHaveBeenCalled();
		});

		it('cancels pending reconnect', () => {
			const connection = connectLogs('my-app', vi.fn());

			mockEventSource.readyState = READY_STATE_CLOSED;
			mockEventSource.onerror?.(new Event('error'));

			expect(instances.length).toBe(1);

			connection.disconnect();
			vi.advanceTimersByTime(3000);

			expect(instances.length).toBe(1);
		});

		it('preserves logs after disconnect', () => {
			const connection = connectLogs('my-app', vi.fn());

			mockEventSource.onmessage?.({ data: 'line 1' } as MessageEvent);
			mockEventSource.onmessage?.({ data: 'line 2' } as MessageEvent);

			connection.disconnect();

			expect(connection.logs).toEqual(['line 1', 'line 2']);
		});
	});

	describe('multiple connections', () => {
		it('each connection is independent', () => {
			const connection1 = connectLogs('app1', vi.fn());
			const es1 = mockEventSource;

			const connection2 = connectLogs('app2', vi.fn());
			const es2 = mockEventSource;

			es1.onmessage?.({ data: 'app1 log' } as MessageEvent);
			es2.onmessage?.({ data: 'app2 log' } as MessageEvent);

			expect(connection1.logs).toEqual(['app1 log']);
			expect(connection2.logs).toEqual(['app2 log']);
		});

		it('disconnecting one does not affect another', () => {
			const connection1 = connectLogs('app1', vi.fn());
			const es1 = mockEventSource;

			const connection2 = connectLogs('app2', vi.fn());

			es1.onopen?.(new Event('open'));
			mockEventSource.onopen?.(new Event('open'));

			connection1.disconnect();

			expect(es1.close).toHaveBeenCalled();
			expect(connection2.connected).toBe(true);
		});
	});
});
