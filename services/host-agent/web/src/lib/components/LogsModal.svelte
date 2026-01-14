<script lang="ts">
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import VirtualLogList from './VirtualLogList.svelte';
	import { connectLogs, type LogsConnection } from '$lib/api/logs';

	interface Props {
		appName: string | null;
		displayName: string;
		onclose: () => void;
	}

	let { appName, displayName, onclose }: Props = $props();

	let connection: LogsConnection | null = $state(null);
	let autoScroll = $state(true);

	$effect(() => {
		if (!appName) return;

		connection = connectLogs(appName, () => {
			// Trigger reactivity by reassigning
			connection = connection;
		});

		return () => {
			connection?.disconnect();
			connection = null;
		};
	});

	function clearLogs() {
		if (!connection) return;
		connection.logs.length = 0;
		connection = connection;
	}
</script>

<Modal open={!!appName} {onclose} size="lg">
	<header class="modal-header">
		<div class="header-title">
			<h2>Logs</h2>
			<span class="app-name">{displayName}</span>
		</div>
		<div class="header-actions">
			<span class="status" class:connected={connection?.connected}>
				{connection?.connected ? 'Live' : 'Connecting...'}
			</span>
			<CloseButton onclick={onclose} />
		</div>
	</header>

	<div class="modal-body">
		{#if connection?.error}
			<div class="error-message">{connection.error}</div>
		{/if}
		<VirtualLogList
			lines={connection?.logs ?? []}
			{autoScroll}
			onautoScrollChange={(v) => (autoScroll = v)}
		/>
	</div>

	<footer class="modal-footer">
		<label class="auto-scroll-toggle">
			<input type="checkbox" bind:checked={autoScroll} />
			Auto-scroll
		</label>
		<button class="btn btn-secondary" onclick={clearLogs}>Clear</button>
	</footer>
</Modal>

<style>
	.modal-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.header-title {
		display: flex;
		align-items: baseline;
		gap: var(--space-sm);
	}

	.header-title h2 {
		margin: 0;
		font-size: 1.125rem;
	}

	.app-name {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: var(--space-md);
	}

	.status {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-subtle);
	}

	.status.connected {
		color: var(--color-success);
		background: rgba(34, 197, 94, 0.1);
	}

	.modal-body {
		padding: 0;
		display: flex;
		flex-direction: column;
	}

	.error-message {
		padding: var(--space-sm) var(--space-md);
		background: rgba(185, 28, 28, 0.1);
		color: var(--color-error);
		font-size: 0.875rem;
	}

	.modal-footer {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-sm) var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.auto-scroll-toggle {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		cursor: pointer;
	}

	.auto-scroll-toggle input {
		cursor: pointer;
	}

	.btn {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-xs) var(--space-md);
		border-radius: var(--radius-md);
		font-size: 0.8125rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn-secondary {
		background: var(--color-bg-elevated);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover:not(:disabled) {
		background: var(--color-bg-subtle);
	}
</style>
