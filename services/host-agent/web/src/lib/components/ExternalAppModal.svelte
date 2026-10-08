<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import Button from './Button.svelte';
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

	let launchers = $state<ExternalApp[]>([]);
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');

	// The add form. A launcher is the only external-app kind today, so the
	// form is name + URL + optional icon, nothing more.
	let name = $state('');
	let url = $state('');
	let icon = $state('');

	// Reload on every open so a launcher added or removed elsewhere is
	// reflected, and a stale list never outlives the visit.
	$effect(() => {
		if (open) void reload();
	});

	async function reload() {
		loading = true;
		error = '';
		try {
			launchers = await fetchExternalApps();
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
</script>

<Modal {open} {onclose} size="lg">
	<header class="modal-header">
		<div>
			<h2>External app</h2>
			<p class="modal-subtitle">
				A shortcut to a site you already use. It becomes a tile on your dashboard that
				opens that URL.
			</p>
		</div>
		<CloseButton onclick={onclose} />
	</header>

	<div class="modal-body">
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

		{#if loading}
			<p class="hint">Loading…</p>
		{:else if launchers.length > 0}
			<ul class="launcher-list">
				{#each launchers as launcher (launcher.id)}
					<li class="launcher-row">
						<div class="launcher-meta">
							<span class="launcher-name">{launcher.name}</span>
							<span class="launcher-url">{launcher.url}</span>
						</div>
						<Button variant="ghost" size="sm" onclick={() => handleRemove(launcher.id)}>
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
		max-width: 44ch;
	}

	.modal-body {
		padding: var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
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

	.launcher-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.launcher-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.launcher-meta {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.launcher-name {
		font-weight: 500;
	}

	.launcher-url {
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
