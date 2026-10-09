<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import Button from './Button.svelte';
	import ExternalProviderForm from './ExternalProviderForm.svelte';
	import {
		fetchExternalApps,
		addExternalApp,
		removeExternalApp,
		type ExternalApp
	} from '$lib/clients/settingsClient';

	interface Props {
		open: boolean;
		onclose: () => void;
	}

	let { open, onclose }: Props = $props();

	let apps = $state<ExternalApp[]>([]);
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');
	let kind = $state<'launcher' | 'provider'>('launcher');

	// The add form. A launcher is name + URL + optional icon, nothing more.
	let name = $state('');
	let url = $state('');
	let icon = $state('');

	// Reload on every open so an app added or removed elsewhere is reflected,
	// and a stale list never outlives the visit.
	$effect(() => {
		if (open) void reload();
	});

	async function reload() {
		loading = true;
		error = '';
		try {
			apps = await fetchExternalApps();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not load external apps';
		} finally {
			loading = false;
		}
	}

	async function handleAdd() {
		if (!name.trim() || !url.trim()) return;
		saving = true;
		error = '';
		try {
			await addExternalApp({ name: name.trim(), url: url.trim(), icon: icon.trim() });
			name = '';
			url = '';
			icon = '';
			await reload();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not add external app';
		} finally {
			saving = false;
		}
	}

	async function handleRemove(id: string) {
		error = '';
		try {
			await removeExternalApp(id);
			await reload();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not remove external app';
		}
	}

	function kindLabel(app: ExternalApp): string {
		if (app.kind === 'provider') return `remote ${app.app ?? 'app'}`;
		return 'launcher';
	}
</script>

<Modal {open} {onclose} size="lg" labelledBy="external-app-title">
	<header class="modal-header">
		<div>
			<h2 id="external-app-title">External app</h2>
			<p class="modal-subtitle">
				Something Bloud does not run: a shortcut to a site you already use, or a
				remote install of a catalog app that keeps wiring to your other apps.
			</p>
		</div>
		<CloseButton onclick={onclose} />
	</header>

	<div class="modal-body">
		<div class="kind-tabs" role="tablist">
			<button
				type="button"
				role="tab"
				class="kind-tab"
				class:active={kind === 'launcher'}
				aria-selected={kind === 'launcher'}
				onclick={() => (kind = 'launcher')}
			>
				Launcher
			</button>
			<button
				type="button"
				role="tab"
				class="kind-tab"
				class:active={kind === 'provider'}
				aria-selected={kind === 'provider'}
				onclick={() => (kind = 'provider')}
			>
				Remote app
			</button>
		</div>

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
			<ExternalProviderForm onsaved={reload} />
		{/if}

		{#if loading}
			<p class="hint">Loading…</p>
		{:else if apps.length > 0}
			<ul class="external-list">
				{#each apps as app (app.id)}
					<li class="external-row">
						<div class="external-meta">
							<span class="external-name">
								{app.name}
								<span class="external-kind">{kindLabel(app)}</span>
							</span>
							<span class="external-url">{app.url}</span>
						</div>
						<Button variant="ghost" size="sm" onclick={() => handleRemove(app.id)}>
							Remove
						</Button>
					</li>
				{/each}
			</ul>
		{/if}

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
		outline: none;
		border-color: var(--color-accent);
	}

	.external-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.external-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.external-meta {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.external-name {
		font-weight: 500;
		display: flex;
		gap: var(--space-sm);
		align-items: baseline;
	}

	.external-kind {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		font-weight: 400;
	}

	.external-url {
		color: var(--color-text-muted);
		font-size: 0.8125rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.hint {
		color: var(--color-text-muted);
		font-size: 0.875rem;
		margin: 0;
	}

	.error {
		color: var(--color-error);
		font-size: 0.875rem;
		margin: 0;
	}
</style>
