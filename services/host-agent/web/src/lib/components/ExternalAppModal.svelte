<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import Button from './Button.svelte';
	import ExternalProviderForm from './ExternalProviderForm.svelte';
	import { addExternalApp } from '$lib/clients/settingsClient';

	interface Props {
		open: boolean;
		onclose: () => void;
	}

	let { open, onclose }: Props = $props();

	let saving = $state(false);
	let error = $state('');

	// Remote app first, and the default. It is the tab that does the work: a
	// remote install is wired to the apps already on this instance, while a
	// launcher is a bookmark. The default should open on the thing the user
	// came here to do, not on the lesser of the two.
	let kind = $state<'provider' | 'launcher'>('provider');

	// The launcher form. A launcher is name + URL + optional icon, nothing more.
	let name = $state('');
	let url = $state('');
	let icon = $state('');

	async function handleAdd() {
		if (!name.trim() || !url.trim()) return;
		saving = true;
		error = '';
		try {
			await addExternalApp({ name: name.trim(), url: url.trim(), icon: icon.trim() });
			name = '';
			url = '';
			icon = '';
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not add external app';
		} finally {
			saving = false;
		}
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
						disabled={saving}
						autocomplete="off"
					/>
					<label for="ext-url">URL</label>
					<input
						id="ext-url"
						type="url"
						placeholder="https://example.com"
						bind:value={url}
						disabled={saving}
						autocomplete="off"
						spellcheck="false"
					/>
					<label for="ext-icon">Icon URL (optional)</label>
					<input
						id="ext-icon"
						type="text"
						placeholder="https://example.com/icon.png"
						bind:value={icon}
						disabled={saving}
						autocomplete="off"
						spellcheck="false"
					/>
					<Button variant="primary" size="sm" type="submit" disabled={saving || !name.trim() || !url.trim()}>
						{saving ? 'Adding…' : 'Add'}
					</Button>
				</form>
			{:else}
				<ExternalProviderForm />
			{/if}
		</div>

		{#if error}
			<p class="error">{error}</p>
		{/if}
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

	.error {
		color: var(--color-error);
		font-size: 0.875rem;
		margin: 0;
	}
</style>
