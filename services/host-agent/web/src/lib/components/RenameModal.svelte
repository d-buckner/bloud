<script lang="ts">
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';

	interface Props {
		appName: string | null;
		currentDisplayName: string;
		onclose: () => void;
		onrename: (appName: string, newDisplayName: string) => void;
	}

	let { appName, currentDisplayName, onclose, onrename }: Props = $props();
	let newName = $state(currentDisplayName);
	let inputEl: HTMLInputElement | undefined = $state();

	$effect(() => {
		if (appName && inputEl) {
			newName = currentDisplayName;
			// Focus and select all text when modal opens
			setTimeout(() => {
				inputEl?.focus();
				inputEl?.select();
			}, 0);
		}
	});

	function doRename() {
		if (!appName || !newName.trim()) return;
		onrename(appName, newName.trim());
		onclose();
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter') {
			doRename();
		}
	}
</script>

<Modal open={appName !== null} {onclose}>
	{#if appName}
		<header class="modal-header">
			<h2>Rename App</h2>
			<CloseButton onclick={onclose} />
		</header>

		<div class="modal-body">
			<label for="rename-input">Display name</label>
			<input
				id="rename-input"
				type="text"
				bind:this={inputEl}
				bind:value={newName}
				onkeydown={handleKeydown}
				placeholder="Enter new name"
			/>
		</div>

		<footer class="modal-footer">
			<button class="btn btn-secondary" onclick={onclose}>Cancel</button>
			<button class="btn btn-primary" onclick={doRename} disabled={!newName.trim()}>
				Rename
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
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.modal-body label {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.modal-body input {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		font-size: 1rem;
		font-family: var(--font-serif);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.modal-body input:focus {
		outline: none;
		border-color: var(--color-accent);
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

	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.btn-secondary {
		background: var(--color-bg-subtle);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover {
		background: var(--color-bg);
	}

	.btn-primary {
		background: var(--color-accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		background: var(--color-accent-hover);
	}
</style>
