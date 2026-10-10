<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// Edit one external app: the launcher's URL, or a remote install's endpoint
	// and the credentials and values it actually owes. The record's *kind* and
	// *source* are not editable, because those say what the record is, and
	// changing what it is means removing it and adding the other thing.
	//
	// The form is generated the same way the add form is, from the provider's
	// `provides:` and the contract registry, and it shows the same trimmed set:
	// only the fields the operator owns. A value the provider's catalog entry
	// declares statically is not shown and is not resent either, so the copy on
	// the record survives a save untouched.
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import Button from './Button.svelte';
	import {
		fetchExternalProviders,
		updateExternalApp,
		type ExternalApp,
		type ExternalProviderContract,
		type ExternalProviderOption
	} from '$lib/clients/settingsClient';
	import { launcherPatch, providerPatch, type ExternalAppForm } from '$lib/utils/externalAppPatch';
	import {
		buildProviderPayload,
		deriveProviderInputs,
		missingProviderInputs,
		readProviderInput,
		type ProviderFieldValues
	} from '$lib/utils/providerInputs';

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
	let fieldValues = $state<ProviderFieldValues>({});
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

	let inputs = $derived(deriveProviderInputs(schema?.contracts ?? []));

	let storedSecrets = $derived(app?.secretContracts ?? []);

	let missing = $derived([
		...(name.trim() ? [] : ['name']),
		...(url.trim() ? [] : ['endpoint']),
		...missingProviderInputs(inputs, fieldValues, storedSecrets)
	]);

	// The schema has to have landed before a save is offered. Not because the
	// save would be destructive -- an omitted credential reads as "keep the
	// one on file", and the server re-validates the values it is sent -- but
	// because a form that can be submitted before it has rendered its own
	// fields teaches the operator that the fields are optional.
	let canSubmit = $derived(missing.length === 0 && !saving && !loadingSchema);

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

	/**
	 * The stored values with the typed ones layered on top, key by key.
	 *
	 * The layering is what keeps the fields this form no longer renders honest.
	 * A static value the catalog answers is not an input here, so it never
	 * appears in the typed payload, and the copy already on the record carries
	 * through instead of being overwritten by an absent key.
	 */
	function mergeTypedValues(
		stored: Record<string, Record<string, string>> | undefined,
		typed: Record<string, Record<string, string>>
	): Record<string, Record<string, string>> {
		const merged = cloneValues(stored);
		for (const [contract, fields] of Object.entries(typed)) {
			merged[contract] = { ...(merged[contract] ?? {}), ...fields };
		}
		return merged;
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
		fieldValues = {};
		if (record.kind === 'provider' && record.app) void loadSchema(record);
	});

	async function loadSchema(record: ExternalApp) {
		loadingSchema = true;
		try {
			providers = await fetchExternalProviders();
			const contracts = providers.find((option) => option.app === record.app)?.contracts ?? [];
			seedContractInputs(record, contracts);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not load the integration schema';
		} finally {
			loadingSchema = false;
		}
	}

	/**
	 * Seed the form from the record's stored values.
	 *
	 * Takes the contracts as an argument rather than reading the derived schema
	 * because it runs in the same turn that writes `providers`, and the seed has
	 * to match exactly what the form is about to render.
	 */
	function seedContractInputs(record: ExternalApp, contracts: ExternalProviderContract[]) {
		const stored = cloneValues(record.values);
		const seeded: ProviderFieldValues = {};
		for (const input of deriveProviderInputs(contracts)) {
			const value = readProviderInput(input, stored);
			if (value !== '') seeded[input.id] = value;
		}
		fieldValues = seeded;
	}

	function secretPlaceholder(input: { contracts: string[] }): string {
		const held = input.contracts.some((contract) => storedSecrets.includes(contract));
		return held ? 'Leave blank to keep the stored credential' : 'Required';
	}

	async function handleSave() {
		if (!app || !canSubmit) return;
		saving = true;
		error = '';
		try {
			const typed = buildProviderPayload(inputs, fieldValues);
			const form: ExternalAppForm = {
				name,
				url,
				icon,
				values: mergeTypedValues(app.values, typed.values),
				secrets: typed.secrets
			};
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

<Modal open={app !== null} {onclose} size="lg" labelledBy="external-app-config-title">
	{#if app}
		<header class="modal-header">
			<div>
				<h2 id="external-app-config-title">Configure {app.name}</h2>
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
				{#each inputs as input (input.id)}
					<label for={`cfg-${input.id}`}>{input.label}</label>
					<input
						id={`cfg-${input.id}`}
						type={input.kind === 'secret' ? 'password' : 'text'}
						placeholder={input.kind === 'secret' ? secretPlaceholder(input) : ''}
						bind:value={fieldValues[input.id]}
						disabled={saving}
						autocomplete="off"
						spellcheck="false"
					/>
					{#if input.help}
						<p class="field-help">{input.help}</p>
					{/if}
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
				border-color: var(--color-accent);
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
