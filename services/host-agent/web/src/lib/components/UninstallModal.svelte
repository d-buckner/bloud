<script lang="ts">
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';

	interface Props {
		appName: string | null;
		onclose: () => void;
		onuninstall: (appName: string) => void;
	}

	let { appName, onclose, onuninstall }: Props = $props();

	function doUninstall() {
		if (!appName) return;
		onuninstall(appName);
		onclose();
	}
</script>

<Modal open={appName !== null} onclose={onclose}>
	{#if appName}
		<header class="modal-header">
			<h2>Remove {appName}?</h2>
			<CloseButton onclick={onclose} />
		</header>

		<div class="modal-body">
			<p>Are you sure you want to remove <strong>{appName}</strong>?</p>
		</div>

		<footer class="modal-footer">
			<button class="btn btn-secondary" onclick={onclose}>Cancel</button>
			<button class="btn btn-danger" onclick={doUninstall}>Remove</button>
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

	.modal-body p {
		margin: 0;
	}

	.modal-footer {
		display: flex;
		gap: var(--space-sm);
		justify-content: flex-end;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.btn {
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn-secondary {
		background: var(--color-bg-subtle);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover {
		background: var(--color-bg);
	}

	.btn-danger {
		background: var(--color-error);
		color: white;
	}

	.btn-danger:hover {
		background: #7f1d1d;
	}
</style>
