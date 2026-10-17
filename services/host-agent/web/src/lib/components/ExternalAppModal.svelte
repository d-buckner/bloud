<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import Button from './Button.svelte';
	import ExternalProviderForm from './ExternalProviderForm.svelte';
	import { useExternalAppActions } from '$lib/store';

	const externalActions = useExternalAppActions();

	interface Props {
		open: boolean;
		onclose: () => void;
		/**
		 * Notified after something was actually added, not on close.
		 *
		 * The dashboard needs it. This modal adds a tile and nothing else re-reads
		 * the external-app records, so a modal that closed without saying so left
		 * the new tile invisible until the next unrelated refresh.
		 */
		onsaved?: () => void;
	}

	let { open, onclose, onsaved }: Props = $props();


	// Remote app first, and the default. It is the tab that does the work: a
	// remote install is wired to the apps already on this instance, while a
	// launcher is a bookmark. The default should open on the thing the user
	// came here to do, not on the lesser of the two.
	let kind = $state<'provider' | 'launcher'>('provider');

	// The launcher form. A launcher is name + URL + optional icon, nothing more.
	let name = $state('');
	let url = $state('');
	let icon = $state('');

	// Added, so get out of the way. The modal only adds, and a dialog that clears
	// its own fields and stays open reads as nothing having happened: the record
	// is saved, the button said "Adding…", and the only evidence is a form that
	// quietly emptied. Closing reveals the tile, which is the confirmation.
	function handleAdded() {
		onsaved?.();
		onclose();
	}

	/**
	 * The store owns the request and the toast. A failure keeps the dialog open so
	 * the operator can correct the field, and the toast says what went wrong.
	 */
	async function handleAdd() {
		if (!name.trim() || !url.trim()) return;
		const result = await externalActions.addLauncher({
			name: name.trim(),
			url: url.trim(),
			icon: icon.trim()
		});
		if (!result.ok) return;
		name = '';
		url = '';
		icon = '';
		handleAdded();
	}
</script>

<Modal {open} {onclose} size="lg" labelledBy="external-app-title">
	<header class="modal-header">
		<div>
			<h2 id="external-app-title">External app</h2>
			<p class="modal-subtitle">
				Something Bloud does not run: a remote install of a catalog app that keeps
				wiring to your other apps, or a shortcut to a site you already use.
			</p>
			<!-- This modal only adds. What is already added is managed where it is
			     used: launchers and remote installs are tiles on the dashboard, and
			     their right-click menu configures and removes them; the inference
			     provider is edited in Settings to AI. A second list here was a second,
			     worse way to do the same thing. -->
		</div>
		<CloseButton onclick={onclose} />
	</header>

	<div class="modal-body">
		<div class="kind-tabs" role="tablist">
			<button
				type="button"
				role="tab"
				id="ext-tab-provider"
				class="kind-tab"
				class:active={kind === 'provider'}
				aria-selected={kind === 'provider'}
				aria-controls="ext-tab-panel"
				onclick={() => (kind = 'provider')}
			>
				Remote app
			</button>
			<button
				type="button"
				role="tab"
				id="ext-tab-launcher"
				class="kind-tab"
				class:active={kind === 'launcher'}
				aria-selected={kind === 'launcher'}
				aria-controls="ext-tab-panel"
				onclick={() => (kind = 'launcher')}
			>
				Launcher
			</button>
		</div>

		<!-- One panel, named by whichever tab is standing: role="tab" with nothing
		     to control is a promise the assistive technology cannot keep. -->
		<div
			role="tabpanel"
			id="ext-tab-panel"
			class="kind-panel"
			aria-labelledby={kind === 'provider' ? 'ext-tab-provider' : 'ext-tab-launcher'}
		>
			{#if kind === 'launcher'}
				<form class="add-form" onsubmit={(e) => { e.preventDefault(); handleAdd(); }}>
					<label for="ext-name">Name</label>
					<input
						id="ext-name"
						type="text"
						placeholder="e.g. My NAS"
						bind:value={name}
						autocomplete="off"
					/>
					<label for="ext-url">URL</label>
					<input
						id="ext-url"
						type="url"
						placeholder="https://example.com"
						bind:value={url}
						autocomplete="off"
						spellcheck="false"
					/>
					<label for="ext-icon">Icon URL (optional)</label>
					<input
						id="ext-icon"
						type="text"
						placeholder="https://example.com/icon.png"
						bind:value={icon}
						autocomplete="off"
						spellcheck="false"
					/>
					<Button variant="primary" size="sm" type="submit" disabled={!name.trim() || !url.trim()}>
						Add
					</Button>
				</form>
			{:else}
				<ExternalProviderForm onsaved={handleAdded} />
			{/if}
		</div>

	</div>
</Modal>

<style>
	.modal-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: var(--space-md);
		padding: var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.modal-header h2 {
		margin: 0;
		font-size: 1.125rem;
	}

	.modal-subtitle {
		margin: var(--space-xs) 0 0;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		max-width: 48ch;
	}

	.modal-body {
		padding: var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.kind-tabs {
		display: flex;
		gap: var(--space-xs);
		border-bottom: 1px solid var(--color-border);
	}

	.kind-tab {
		background: none;
		border: none;
		border-bottom: 2px solid transparent;
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.875rem;
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.kind-tab.active {
		color: var(--color-text);
		border-bottom-color: var(--color-accent);
	}

	.add-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.add-form label {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.add-form input {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.add-form input:focus {
				border-color: var(--color-accent);
	}

	.kind-panel {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

</style>
