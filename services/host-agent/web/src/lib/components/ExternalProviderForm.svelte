<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Button from './Button.svelte';
	import {
		fetchExternalProviders,
		addExternalProvider,
		type ExternalProviderOption
	} from '$lib/clients/settingsClient';
	import {
		buildExchangePayload,
		buildProviderPayload,
		defaultUseStoredLogin,
		deriveExchangeInputs,
		deriveProviderInputs,
		missingExchangeInputs,
		missingProviderInputs,
		type ProviderFieldValues
	} from '$lib/utils/providerInputs';

	interface Props {
		onsaved?: () => void;
	}

	let { onsaved }: Props = $props();

	let options = $state<ExternalProviderOption[]>([]);
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');

	let selected = $state('');
	let url = $state('');
	let fieldValues = $state<ProviderFieldValues>({});
	let useStoredLogin = $state(false);

	let current = $derived(options.find((o) => o.app === selected));
	let selectable = $derived(options.filter((o) => !o.installed));

	// Named for the app, not for where it lives. A remote Jellyfin is still
	// Jellyfin: the tile wears its icon and carries its name, and nothing on
	// the grid needs to announce that this one is not a container Bloud
	// booted. The record keeps the distinction; the dashboard does not. And
	// because the catalog already answers it, the form does not ask: a field
	// prefilled with the only thing it can hold is a field that gets skipped,
	// not read. Renaming is the Configure modal's job.
	let name = $derived(current?.displayName ?? '');

	// The whole form, derived. Not every field the registry knows, but the
	// subset this operator actually owns: static catalog facts and optional
	// values are left out, and one credential covers every contract of the
	// app that declares the same secret name. For a remote Sonarr that is the
	// endpoint and one API key, and nothing else.
	let inputs = $derived(deriveProviderInputs(current?.contracts ?? []));

	// The sign-in half, for the one kind of app that cannot be configured with a
	// pasted key: a remote Seerr's credential is an API key nobody has ever
	// looked at, but its admin is a username and password. The server says which
	// fields that means and whether it already holds the login, so this component
	// still does not know what a Seerr is.
	let exchangeInputs = $derived(deriveExchangeInputs(current?.exchange, useStoredLogin));

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

	// Choosing the app seeds the form from the generated schema rather than
	// from a per-app definition written here: the contract registry says what
	// a provider must publish, so this component never learns what AFFiNE is.
	function chooseApp(app: string) {
		selected = app;
		fieldValues = {};
		useStoredLogin = defaultUseStoredLogin(options.find((o) => o.app === app)?.exchange);
	}

	const canSubmit = $derived(
		!!selected &&
			!!name.trim() &&
			!!url.trim() &&
			missingProviderInputs(inputs, fieldValues).length === 0 &&
			missingExchangeInputs(exchangeInputs, fieldValues, useStoredLogin).length === 0
	);

	async function handleAdd() {
		if (!canSubmit || saving) return;
		saving = true;
		error = '';
		try {
			const payload = buildProviderPayload(inputs, fieldValues);
			const signin = buildExchangePayload(exchangeInputs, fieldValues, useStoredLogin);
			await addExternalProvider({
				app: selected,
				name: name.trim(),
				url: url.trim(),
				values: payload.values,
				secrets: payload.secrets,
				exchange: signin.exchange,
				exchangeLogin: signin.exchangeLogin
			});
			selected = '';
			url = '';
			fieldValues = {};
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

		<label for="prov-url">Endpoint</label>
		<input
			id="prov-url"
			type="text"
			placeholder="https://radarr.example.com"
			bind:value={url}
			disabled={saving || !selected}
			autocomplete="off"
			spellcheck="false"
		/>
		<p class="field-help">The origin only, with no path. Bloud appends what each integration needs.</p>

		{#each inputs as input (input.id)}
			<label for={`prov-${input.id}`}>{input.label}</label>
			<input
				id={`prov-${input.id}`}
				type={input.kind === 'secret' ? 'password' : 'text'}
				bind:value={fieldValues[input.id]}
				disabled={saving}
				autocomplete="off"
				spellcheck="false"
			/>
			{#if input.help}
				<p class="field-help">{input.help}</p>
			{/if}
		{/each}

		{#if current?.exchange}
			{#if current.exchange.storedLogin}
				<label class="checkbox" for="prov-stored-login">
					<input id="prov-stored-login" type="checkbox" bind:checked={useStoredLogin} disabled={saving} />
					{current.exchange.storedLogin}
				</label>
			{/if}
			{#each exchangeInputs as input (input.id)}
				<label for={`prov-${input.id}`}>{input.label}</label>
				<input
					id={`prov-${input.id}`}
					type={input.secret ? 'password' : 'text'}
					bind:value={fieldValues[input.id]}
					disabled={saving}
					autocomplete="off"
					spellcheck="false"
				/>
				{#if input.help}
					<p class="field-help">{input.help}</p>
				{/if}
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

	.field-help {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	/* A checkbox reads as a statement, not a field: the label sits beside the box
	   rather than above it, which is what tells it apart from the inputs the
	   operator still has to fill. */
	.checkbox {
		flex-direction: row;
		align-items: center;
		gap: var(--space-sm);
		font-size: 0.875rem;
		color: var(--color-text);
	}

	.checkbox input {
		width: auto;
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
