/**
 * Logs SSE Client - Manages streaming log connections for apps
 */

const MAX_LOGS = 1000;
const TRIM_TO = 500;
const RECONNECT_DELAY_MS = 3000;

export interface LogsConnection {
	readonly logs: string[];
	readonly connected: boolean;
	readonly error: string | null;
	disconnect: () => void;
}

interface LogsConnectionState {
	logs: string[];
	connected: boolean;
	error: string | null;
	eventSource: EventSource | null;
	reconnectTimeout: ReturnType<typeof setTimeout> | null;
	onUpdate: () => void;
}

/**
 * Connect to the logs SSE endpoint for a specific app
 */
export function connectLogs(appName: string, onUpdate: () => void): LogsConnection {
	const state: LogsConnectionState = {
		logs: [],
		connected: false,
		error: null,
		eventSource: null,
		reconnectTimeout: null,
		onUpdate
	};

	connect(appName, state);

	return {
		get logs() {
			return state.logs;
		},
		get connected() {
			return state.connected;
		},
		get error() {
			return state.error;
		},
		disconnect: () => disconnect(state)
	};
}

function connect(appName: string, state: LogsConnectionState): void {
	disconnect(state);

	state.eventSource = new EventSource(`/api/apps/${appName}/logs`);

	state.eventSource.onopen = () => {
		state.connected = true;
		state.error = null;
		state.onUpdate();
	};

	state.eventSource.onmessage = (e) => {
		state.logs.push(e.data);
		if (state.logs.length > MAX_LOGS) {
			state.logs.splice(0, state.logs.length - TRIM_TO);
		}
		state.onUpdate();
	};

	state.eventSource.onerror = () => {
		if (state.eventSource?.readyState === EventSource.CLOSED) {
			state.error = 'Connection closed';
			state.connected = false;
			state.onUpdate();

			state.reconnectTimeout = setTimeout(() => {
				connect(appName, state);
			}, RECONNECT_DELAY_MS);
		}
	};
}

function disconnect(state: LogsConnectionState): void {
	if (state.reconnectTimeout) {
		clearTimeout(state.reconnectTimeout);
		state.reconnectTimeout = null;
	}
	if (state.eventSource) {
		state.eventSource.close();
		state.eventSource = null;
	}
}
