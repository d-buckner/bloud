<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// Edit one external app: the launcher's URL, or a remote install's endpoint,
	// contract values, and credentials. The record's *kind* and *source* are not
	// editable, because those say what the record is, and changing what it is
	// means removing it and adding the other thing.
	//
	// The form is generated the same way the add form is: from the provider's
	// `provides:` and the contract registry, never from a per-app definition
	// written here. What differs is only where each field is seeded from: the
	// stored record rather than the catalog default.
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import Button from './Button.svelte';
	import {
		fetchExternalProviders,
		updateExternalApp,
		type ExternalApp,
		type ExternalProviderOption
	} from '$lib/clients/settingsClient';
	import {
		launcherPatch,
		missingRequiredFields,
		providerPatch,
		type ExternalAppForm
	} from '$lib/utils/externalAppPatch';

	interface Props {
		app: ExternalApp | null;
		onclose: () => void;
		onsaved?: () => void;
	}

	let { app, onclose, onsaved }: Props = $props();

	let saving = $state(false);
	let error = $state('');
	let loadingSchema = $state(false);

	let name = $state('');
	let url = $state('');
	let icon = $state('');
	let values = $state<Record<string, Record<string, string>>>({});
	let secrets = $state<Record<string, string>>({});
	let providers = $state<ExternalProviderOption[]>([]);

	let isProvider = $derived(app?.kind === 'provider');
	let endpointLabel = $derived(isProvider ? 'Endpoint' : 'URL');
	let endpointPlaceholder = $derived(isProvider ? 'https://jellyfin.example.com' : 'https://example.com');
	let endpointHelp = $derived(
		isProvider
			? 'The origin only, with no path. Bloud appends what each integration needs.'
			: 'A launcher opens exactly this, in a new tab.'
	);

	/**
	 * The catalog schema behind this record, when it stands in for a catalog app.
	 * A bare contract provider (an AI endpoint, owned by Settings -> AI) has no
	 * catalog entry to ask, and never reaches this modal as a tile anyway.
	 */
	let schema = $derived(providers.find((option) => option.app === app?.app) ?? null);

	/**
	 * Required fields still empty. A stored secret counts as satisfying its own
	 * requirement: the list response never echoes the value back, so the form
	 * cannot re-supply one it is only being asked to confirm.
	 */
	let form = $derived<ExternalAppForm>({ name, url, icon, values, secrets });

	let requiredFields = $derived(
		(schema?.contracts ?? []).flatMap((contract) =>
			contract.fields
				.filter((field) => field.required)
				.map((field) => ({
					contract: contract.name,
					key: field.key,
					label: field.label,
					kind: field.kind
				}))
		)
	);

	let missing = $derived(
		missingRequiredFields(form, isProvider ? requiredFields : [], app?.secretContracts ?? [])
	);

	let canSubmit = $derived(missing.length === 0 && !saving);

	function hasStoredSecret(contract: string): boolean {
		return (app?.secretContracts ?? []).includes(contract);
	}

	/**
	 * A copy-on-write copy of the stored contract values, so editing the form
	 * cannot mutate the record in the list store.
	 *
	 * Deliberately not `structuredClone`: the record arrives out of Svelte state,
	 * where the nested objects are proxies, and `structuredClone` throws on them
	 * ("could not be cloned") rather than copying. A two-level spread is all the
	 * shape needs, because contract values are a map of maps of strings and go no
	 * deeper.
	 */
	function cloneValues(stored: Record<string, Record<string, string>> | undefined) {
		const copy: Record<string, Record<string, string>> = {};
		for (const [contract, fields] of Object.entries(stored ?? {})) {
			copy[contract] = { ...fields };
		}
		return copy;
	}

	// Seeded from the record, and re-seeded only when the record *identity*
	// changes. A form that re-seeded on every store refresh would wipe what the
	// operator is halfway through typing; one that never re-seeded would show
	// one record's fields while saving another's.
	let seededFor = $state('');

	$effect(() => {
		const record = app;
		if (!record || record.id === seededFor) return;
		seededFor = record.id;
		name = record.name;
		url = record.url;
		icon = record.icon;
		values = cloneValues(record.values);
		secrets = Object.fromEntries(Object.keys(values).map((contract) => [contract, '']));
		if (record.kind === 'provider' && record.app) void loadSchema();
	});

	async function loadSchema() {
		loadingSchema = true;
		try {
			providers = await fetchExternalProviders();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not load the integration schema';
		} finally {
			loadingSchema = false;
		}
	}

	function secretPlaceholder(contract: string): string {
		return hasStoredSecret(contract) ? 'Leave blank to keep the stored credential' : 'Required';
	}

	async function handleSave() {
		if (!app || !canSubmit) return;
		saving = true;
		error = '';
		try {
			await updateExternalApp(app.id, isProvider ? providerPatch(form) : launcherPatch(form));
			onsaved?.();
			onclose();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not save the change';
		} finally {
			saving = false;
		}
	}
</script>

<Modal open={app !== null} {onclose} size="lg">
	{#if app}
		<header class="modal-header">
			<div>
				<h2>Configure {app.name}</h2>
				<p class="modal-subtitle">
					{#if isProvider}
						A remote install of {schema?.displayName || app.app || 'an app'} that Bloud does not
						run. Changing its endpoint re-wires every app that integrates with it.
					{:else}
						A launcher tile. It opens a URL and wires to nothing.
					{/if}
				</p>
			</div>
			<CloseButton onclick={onclose} />
		</header>

		<form class="modal-body" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
			<label for="cfg-name">Name</label>
			<input id="cfg-name" type="text" bind:value={name} disabled={saving} autocomplete="off" />

			<label for="cfg-url">{endpointLabel}</label>
			<input
				id="cfg-url"
				type="text"
				placeholder={endpointPlaceholder}
				bind:value={url}
				disabled={saving}
				autocomplete="off"
				spellcheck="false"
			/>
			<p class="field-help">{endpointHelp}</p>

			{#if !isProvider}
				<label for="cfg-icon">Icon URL (optional)</label>
				<input
					id="cfg-icon"
					type="text"
					placeholder="https://example.com/icon.png"
					bind:value={icon}
					disabled={saving}
					autocomplete="off"
					spellcheck="false"
				/>
			{:else if loadingSchema}
				<p class="hint">Loading the integration schema…</p>
			{:else if schema}
				{#each schema.contracts as contract (contract.name)}
					<fieldset class="contract-block">
						<legend>{contract.name}</legend>
						{#each contract.fields as field (field.key)}
							<label for={`cfg-${contract.name}-${field.key}`}>
								{field.label}
								{#if !field.required}<span class="optional">optional</span>{/if}
							</label>
							{#if field.kind === 'secret'}
								<input
									id={`cfg-${contract.name}-${field.key}`}
									type="password"
									placeholder={secretPlaceholder(contract.name)}
									bind:value={secrets[contract.name]}
									disabled={saving}
									autocomplete="off"
									spellcheck="false"
								/>
							{:else}
								<input
									id={`cfg-${contract.name}-${field.key}`}
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
			{:else}
				<p class="hint">
					{app.app} is no longer in the catalog, so its integration fields cannot
					be edited here. The name and endpoint still save.
				</p>
			{/if}

			{#if error}
				<p class="error">{error}</p>
			{/if}

			<footer class="modal-footer">
				<Button variant="ghost" size="sm" type="button" onclick={onclose}>Cancel</Button>
				<Button variant="primary" size="sm" type="submit" disabled={!canSubmit}>
					{saving ? 'Saving…' : 'Save'}
				</Button>
			</footer>
		</form>
	{/if}
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
		max-width: 52ch;
	}

	.modal-body {
		padding: var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.modal-body label {
		font-size: 0.875rem;
		color: var(--color-text-muted);
		display: flex;
		gap: var(--space-sm);
		align-items: baseline;
	}

	.modal-body input {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.modal-body input:focus {
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
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.error {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-error);
	}

	.modal-footer {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-sm);
		margin-top: var(--space-md);
	}
</style>
