<script lang="ts">
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import Icon from './Icon.svelte';
	import type { RollbackResult } from '$lib/types';

	interface Props {
		open: boolean;
		onclose: () => void;
		onrollback: () => void;
	}

	let { open, onclose, onrollback }: Props = $props();

	let rollingBack = $state(false);
	let rollbackResult = $state<RollbackResult | null>(null);

	async function doRollback() {
		rollingBack = true;
		rollbackResult = null;

		try {
			const res = await fetch('/api/system/rollback', { method: 'POST' });
			rollbackResult = await res.json();

			if (rollbackResult?.success) {
				onrollback();
			}
		} catch (err) {
			rollbackResult = {
				success: false,
				output: '',
				errorMessage: err instanceof Error ? err.message : 'Rollback failed',
				duration: '0s'
			};
		} finally {
			rollingBack = false;
		}
	}

	function handleClose() {
		rollbackResult = null;
		onclose();
	}
</script>

<Modal {open} onclose={handleClose}>
	<header class="modal-header">
		<h2>Rollback System</h2>
		<CloseButton onclick={handleClose} />
	</header>

	{#if rollbackResult}
		<div class="modal-body result-view">
			{#if rollbackResult.success}
				<div class="result-icon success">
					<Icon name="check-circle" size={48} />
				</div>
				<h3>Rollback Complete</h3>
				<p>System reverted to previous generation</p>
			{:else}
				<div class="result-icon error">
					<Icon name="error-circle" size={48} />
				</div>
				<h3>Rollback Failed</h3>
				<p class="error-message">{rollbackResult.errorMessage}</p>
			{/if}

			<button class="btn btn-primary" onclick={handleClose}>Done</button>
		</div>
	{:else}
		<div class="modal-body">
			<div class="alert alert-warning">
				<p><strong>Warning:</strong> This will revert all apps to the state of the previous NixOS generation.</p>
				<p>Any apps installed or removed since the last transaction will be reverted.</p>
			</div>
		</div>

		<footer class="modal-footer">
			<button class="btn btn-secondary" onclick={handleClose}>Cancel</button>
			<button class="btn btn-primary" onclick={doRollback} disabled={rollingBack}>
				{#if rollingBack}Rolling back...{:else}Confirm Rollback{/if}
			</button>
		</footer>
	{/if}
</Modal>

<style>
	.modal-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.modal-header h2 {
		margin: 0;
		font-size: 1.125rem;
	}

	.modal-body {
		padding: var(--space-lg);
	}

	.modal-footer {
		display: flex;
		gap: var(--space-sm);
		justify-content: flex-end;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.result-view {
		text-align: center;
		padding: var(--space-xl) var(--space-lg);
	}

	.result-icon { margin-bottom: var(--space-md); }
	.result-icon.success { color: var(--color-success); }
	.result-icon.error { color: var(--color-error); }

	.result-view h3 {
		margin: 0 0 var(--space-md) 0;
		font-size: 1.25rem;
	}

	.error-message {
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
	}

	.result-view .btn {
		margin-top: var(--space-md);
	}

	.alert {
		padding: var(--space-md);
		border-radius: var(--radius-md);
		margin-bottom: var(--space-md);
		font-size: 0.9375rem;
	}

	.alert p { margin: var(--space-sm) 0; }
	.alert p:first-child { margin-top: 0; }
	.alert p:last-child { margin-bottom: 0; }

	.alert-warning {
		background: var(--color-warning-bg);
		color: var(--color-warning);
	}

	.btn {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn-primary {
		background: var(--color-accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		background: var(--color-accent-hover);
	}

	.btn-primary:disabled {
		opacity: 0.6;
		cursor: not-allowed;
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
