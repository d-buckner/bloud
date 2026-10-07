<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// ClientAccessModal wraps ClientAccessPanel in a modal, opened from the app
	// tile's right-click menu. The panel is the reveal/rotate surface; the modal
	// supplies the chrome and the description, so the "what" stays in one
	// component and this is only a "where".
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import ClientAccessPanel from './ClientAccessPanel.svelte';

	interface Props {
		appName: string | null;
		displayName: string;
		onclose: () => void;
	}

	let { appName, displayName, onclose }: Props = $props();
</script>

<Modal open={appName !== null} {onclose}>
	{#if appName}
		<header class="modal-header">
			<h2>Client access</h2>
			<CloseButton onclick={onclose} />
		</header>

		<div class="modal-body">
			<p class="modal-description">
				Credentials a client you hold, like a phone app, signs in with for
				<strong>{displayName}</strong>. A browser still signs in through the
				identity provider; this is the separate credential for clients that
				cannot.
			</p>
			<ClientAccessPanel {appName} />
		</div>
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

	.modal-description {
		margin: 0 0 var(--space-lg) 0;
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		line-height: 1.5;
	}
</style>
