<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	/**
	 * DeleteUserModal: the last gate before a user account is removed.
	 *
	 * It is an `alertdialog` because the action is destructive and irreversible,
	 * and it names the account in the title, in the consequence, and on the
	 * confirm button. A dialog that asks "Delete this user?" while a row further
	 * down was clicked leaves the operator to do the matching, which is how the
	 * wrong account gets deleted. Focus lands on Cancel, because the dialog
	 * opened on a row that was clicked, not on a decision that has been made.
	 */
	import Modal from './Modal.svelte';

	interface Props {
		/** The account named in the dialog; null closes it. */
		username: string | null;
		error?: string | null;
		deleting?: boolean;
		onclose: () => void;
		onconfirm: (username: string) => void;
	}

	let { username, error = null, deleting = false, onclose, onconfirm }: Props = $props();
</script>

<Modal
	open={username !== null}
	onclose={onclose}
	labelledBy="delete-user-title"
	describedBy="delete-user-consequence"
	dialogRole="alertdialog"
>
	{#if username}
		<header class="confirm-header">
			<h2 id="delete-user-title">Delete {username}?</h2>
		</header>

		<div class="confirm-body">
			<p id="delete-user-consequence">
				<strong>{username}</strong> loses access to this instance immediately,
				their open sessions are ended, and their saved dashboard layout is removed.
				This cannot be undone.
			</p>
			{#if error}
				<p class="confirm-error">{error}</p>
			{/if}
		</div>

		<footer class="confirm-footer">
			<button class="btn btn-secondary" onclick={onclose} data-autofocus>
				Cancel
			</button>
			<button
				class="btn btn-danger"
				onclick={() => username && onconfirm(username)}
				disabled={deleting}
			>
				{deleting ? 'Deleting...' : `Delete ${username}`}
			</button>
		</footer>
	{/if}
</Modal>

<style>
	/* The two button variants this dialog uses are copied from the section that
	   opens it: Svelte scopes a component's styles to its own markup, so a
	   settings section's `.btn` does not reach a child component's button. */
	.btn {
		padding: var(--space-sm) var(--space-lg);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		cursor: pointer;
		transition: all 0.15s ease;
		align-self: flex-start;
	}

	.btn:disabled {
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

	.btn-danger {
		background: var(--color-error);
		color: white;
	}

	.btn-danger:hover:not(:disabled) {
		background: #7f1d1d;
	}

	.confirm-header {
		padding: var(--space-lg) var(--space-lg) 0 var(--space-lg);
	}

	.confirm-header h2 {
		margin: 0;
		font-size: 1.125rem;
	}

	.confirm-body {
		padding: var(--space-md) var(--space-lg);
		font-size: 0.9375rem;
		line-height: 1.5;
		color: var(--color-text-secondary);
	}

	.confirm-body p {
		margin: 0;
	}

	.confirm-body p + p {
		margin-top: var(--space-md);
	}

	.confirm-error {
		color: var(--color-error);
	}

	.confirm-footer {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-sm);
		padding: 0 var(--space-lg) var(--space-lg) var(--space-lg);
	}
</style>
