<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import {
		fetchExternalApps,
		addExternalApp,
		removeExternalApp,
		type ExternalApp
	} from '$lib/clients/settingsClient';
	import Button from '$lib/components/Button.svelte';

	let launchers = $state<ExternalApp[]>([]);
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');

	// The add form. A launcher is the only external-app kind in this PR, so the
	// form is name + URL + optional icon, nothing more.
	let name = $state('');
	let url = $state('');
	let icon = $state('');

	onMount(async () => {
		await reload();
	});

	async function reload() {
		loading = true;
		error = '';
		try {
			launchers = await fetchExternalApps();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not load launchers';
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
			error = e instanceof Error ? e.message : 'Could not add launcher';
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
			error = e instanceof Error ? e.message : 'Could not remove launcher';
		}
	}
</script>

<section class="section">
	<h2>Launchers</h2>
	<p class="section-description">
		Shortcuts to sites you already use. A launcher is a tile, not an app Bloud runs.
	</p>

	{#if loading}
		<p class="hint">Loading launchers...</p>
	{:else}
		{#if launchers.length === 0}
			<p class="hint">No launchers yet.</p>
		{:else}
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

		<form class="add-form" onsubmit={(e) => { e.preventDefault(); handleAdd(); }}>
			<input type="text" placeholder="Name" bind:value={name} disabled={saving} autocomplete="off" />
			<input
				type="url"
				placeholder="https://example.com"
				bind:value={url}
				disabled={saving}
				autocomplete="off"
				spellcheck="false"
			/>
			<input
				type="text"
				placeholder="Icon URL (optional)"
				bind:value={icon}
				disabled={saving}
				autocomplete="off"
				spellcheck="false"
			/>
			<Button variant="primary" size="sm" type="submit" disabled={saving || !name.trim() || !url.trim()}>
				{saving ? 'Adding…' : 'Add launcher'}
			</Button>
		</form>
	{/if}

	{#if error}
		<p class="error">{error}</p>
	{/if}
</section>

<style>
	.section {
		margin-bottom: var(--space-2xl);
	}

	h2 {
		margin: 0 0 var(--space-xs);
		font-size: 1.25rem;
		font-weight: 500;
	}

	.section-description {
		margin: 0 0 var(--space-lg);
		color: var(--color-text-muted);
		font-size: 0.875rem;
		max-width: 60ch;
	}

	.launcher-list {
		list-style: none;
		margin: 0 0 var(--space-lg);
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
		background: var(--color-bg-elevated);
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

	.add-form {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		align-items: center;
	}

	input {
		flex: 1 1 180px;
		padding: var(--space-sm);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		font-family: var(--font-sans);
		font-size: 0.875rem;
	}

	input:focus {
		outline: 2px solid var(--color-accent);
		outline-offset: 1px;
	}

	.hint {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.error {
		color: var(--color-error);
		font-size: 0.875rem;
		margin-top: var(--space-sm);
	}
</style>
