<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// Confirmation for dropping one external app record. Deliberately its own
	// modal rather than a reuse of UninstallModal: an uninstall tears down
	// containers and a data directory Bloud owns, and this tears down neither.
	// Saying the wrong one of those two things is how an operator either trusts
	// the dialog too much or refuses a change that was safe.
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';

	interface Props {
		target: { name: string; isProvider: boolean } | null;
		onclose: () => void;
		onremove: () => void;
	}

	let { target, onclose, onremove }: Props = $props();

	let what = $derived(target?.isProvider ? 'remote app' : 'launcher');

	function doRemove() {
		if (!target) return;
		onremove();
		onclose();
	}
</script>

<Modal
	open={target !== null}
	{onclose}
	labelledBy="external-app-remove-title"
	describedBy="external-app-remove-consequence"
	dialogRole="alertdialog"
>
	{#if target}
		<header class="modal-header">
			<h2 id="external-app-remove-title">Remove {target.name}?</h2>
			<CloseButton onclick={onclose} />
		</header>

		<div class="modal-body">
			<p id="external-app-remove-consequence">
				Bloud stops pointing at this <strong>{what}</strong>. Any app wired to it
				loses that integration on the next reconciliation pass.
			</p>
			<p class="reassurance">
				Nothing on the remote machine is touched. A credential Bloud created over
				there stays until you revoke it in that app's own settings.
			</p>
		</div>

		<footer class="modal-footer">
			<button class="btn btn-secondary" onclick={onclose} data-autofocus>Cancel</button>
			<button class="btn btn-danger" onclick={doRemove}>Remove</button>
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

	.modal-body p {
		margin: 0;
	}

	.reassurance {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
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
		border-color: var(--color-error);
	}

	.btn-danger:hover {
		opacity: 0.9;
	}
</style>
