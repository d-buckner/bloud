<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Button from './Button.svelte';
	import {
		fetchExternalProviders,
		addExternalProvider,
		type ExternalProviderOption
	} from '$lib/clients/settingsClient';

	interface Props {
		onsaved?: () => void;
	}

	let { onsaved }: Props = $props();

	let options = $state<ExternalProviderOption[]>([]);
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');

	let selected = $state('');
	let name = $state('');
	let url = $state('');
	let values = $state<Record<string, Record<string, string>>>({});
	let secrets = $state<Record<string, string>>({});

	let current = $derived(options.find((o) => o.app === selected));
	let selectable = $derived(options.filter((o) => !o.installed));

	$effect(() => {
		void load();
	});

	async function load() {
		loading = true;
		try {
			options = await fetchExternalProviders();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not load the catalog';
		} finally {
			loading = false;
		}
	}

	// Choosing the app seeds the whole form from the generated schema rather
	// than from a per-app definition written here: the contract registry says
	// what a provider must publish, so this component never learns what AFFiNE
	// is.
	function chooseApp(app: string) {
		selected = app;
		const option = options.find((o) => o.app === app);
		if (!option) return;
		name = `${option.displayName} (remote)`;
		values = {};
		secrets = {};
		for (const contract of option.contracts) {
			values[contract.name] = {};
			secrets[contract.name] = '';
			for (const field of contract.fields) {
				if (field.kind === 'value') values[contract.name][field.key] = '';
			}
		}
	}

	const requiredValueFields = $derived(
		(current?.contracts ?? []).flatMap((c) => c.fields.filter(
			(f) => f.kind === 'value' && f.required && !(values[c.name]?.[f.key] ?? '').trim()
		))
	);
	const requiredSecretFields = $derived(
		(current?.contracts ?? []).flatMap((c) =>
			c.fields.filter((f) => f.kind === 'secret' && f.required && !(secrets[c.name] ?? '').trim())
		)
	);
	const canSubmit = $derived(
		!!selected &&
			!!name.trim() &&
			!!url.trim() &&
			requiredValueFields.length === 0 &&
			requiredSecretFields.length === 0
	);

	async function handleAdd() {
		if (!canSubmit || saving) return;
		saving = true;
		error = '';
		try {
			await addExternalProvider({
				app: selected,
				name: name.trim(),
				url: url.trim(),
				values,
				secrets
			});
			selected = '';
			name = '';
			url = '';
			values = {};
			secrets = {};
			onsaved?.();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not add the remote app';
		} finally {
			saving = false;
		}
	}
</script>

{#if loading}
	<p class="hint">Loading the catalog…</p>
{:else if selectable.length === 0}
	<p class="hint">
		Every app that can be pointed at a remote instance is already installed here.
	</p>
{:else}
	<form class="provider-form" onsubmit={(e) => { e.preventDefault(); handleAdd(); }}>
		<label for="prov-app">App</label>
		<select id="prov-app" value={selected} onchange={(e) => chooseApp(e.currentTarget.value)} disabled={saving}>
			<option value="">Choose an app…</option>
			{#each selectable as option (option.app)}
				<option value={option.app}>{option.displayName}</option>
			{/each}
		</select>

		<label for="prov-name">Name</label>
		<input id="prov-name" type="text" bind:value={name} disabled={saving || !selected} autocomplete="off" />

		<label for="prov-url">Endpoint</label>
		<input
			id="prov-url"
			type="text"
			placeholder="https://affine.example.com"
			bind:value={url}
			disabled={saving || !selected}
			autocomplete="off"
			spellcheck="false"
		/>
		<p class="field-help">The origin only, with no path. Bloud appends what each integration needs.</p>

		{#if current}
			{#each current.contracts as contract (contract.name)}
				<fieldset class="contract-block">
					<legend>{contract.name}</legend>
					{#each contract.fields as field (field.key)}
						<label for={`prov-${contract.name}-${field.key}`}>
							{field.label}
							{#if !field.required}<span class="optional">optional</span>{/if}
						</label>
						{#if field.kind === 'secret'}
							<input
								id={`prov-${contract.name}-${field.key}`}
								type="password"
								bind:value={secrets[contract.name]}
								disabled={saving}
								autocomplete="off"
								spellcheck="false"
							/>
						{:else}
							<input
								id={`prov-${contract.name}-${field.key}`}
								type="text"
								bind:value={values[contract.name][field.key]}
								disabled={saving}
								autocomplete="off"
								spellcheck="false"
							/>
						{/if}
						{#if field.help}
							<p class="field-help">{field.help}</p>
						{/if}
					{/each}
				</fieldset>
			{/each}
		{/if}

		{#if error}
			<p class="error">{error}</p>
		{/if}

		<Button variant="primary" size="sm" type="submit" disabled={!canSubmit || saving}>
			{saving ? 'Adding…' : 'Add remote app'}
		</Button>
	</form>
{/if}

<style>
	.provider-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.provider-form label {
		font-size: 0.875rem;
		color: var(--color-text-muted);
		display: flex;
		gap: var(--space-sm);
		align-items: baseline;
	}

	.provider-form input,
	.provider-form select {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.provider-form input:focus,
	.provider-form select:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.contract-block {
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		padding: var(--space-md);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin: var(--space-xs) 0 0;
	}

	.contract-block legend {
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		padding: 0 var(--space-xs);
	}

	.optional {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.field-help {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
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
