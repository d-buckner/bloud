<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import {
		discoverEndpointModels,
		fetchEndpointSettings,
		setEndpointSettings,
		type EndpointContract,
		type EndpointSettings
	} from '$lib/clients/settingsClient';
	import { appliedTo, buildSaveRequest, SAVE_WINDOW } from '$lib/utils/endpointSave';
	import Button from '$lib/components/Button.svelte';
	import { pollUntil } from '$lib/store/effects/convergence';

	interface Props {
		/** Which single-endpoint contract this form reads and writes. */
		contract: EndpointContract;
		/** The sub-heading, and the label every message about this row uses. */
		label: string;
		/** What the endpoint does, in the operator's terms. */
		description: string;
		/** The name the API stores when the operator left the field blank. */
		defaultName: string;
		/** Example address shown in the Base URL field. */
		placeholder: string;
		/**
		 * The AI upstream address, offered as a one-click fill. Optional and
		 * never a dependency: the parent may not have loaded it, and a missing
		 * convenience is not a reason this form cannot be filled by hand.
		 */
		aiBaseUrl?: string;
	}

	let { contract, label, description, defaultName, placeholder, aiBaseUrl = '' }: Props = $props();

	let settings = $state<EndpointSettings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	let removing = $state(false);
	let error = $state('');
	let saved = $state(false);

	// Seeded in onMount rather than from `defaultName` here: a $state initialized
	// from a prop captures only its initial value, and the field is re-seeded
	// from the server on every load and every save anyway.
	let name = $state('');
	let baseUrl = $state('');
	let apiKey = $state('');
	let model = $state('');
	let models = $state<string[]>([]);
	let listing = $state(false);
	let listResult = $state('');
	let listOk = $state<boolean | null>(null);

	let dirty = $derived(
		name !== (settings?.name ?? defaultName) ||
			baseUrl !== (settings?.baseUrl ?? '') ||
			model !== (settings?.model ?? '') ||
			apiKey !== ''
	);
	let configured = $derived(settings?.configured ?? false);
	let hasApiKey = $derived(settings?.hasApiKey ?? false);
	let servedTo = $derived(settings?.servedTo ?? []);

	onMount(async () => {
		try {
			settings = await fetchEndpointSettings(contract);
			name = settings.name || defaultName;
			baseUrl = settings.baseUrl;
			model = settings.model;
			// The stored list is not returned (there is none: the endpoint owns
			// its models), so the picker starts empty until the operator lists.
		} catch (e) {
			error = e instanceof Error ? e.message : `Could not load ${label.toLowerCase()} settings`;
		} finally {
			loading = false;
		}
	});

	async function handleListModels() {
		if (!baseUrl.trim()) return;
		listing = true;
		listResult = '';
		listOk = null;
		try {
			const res = await discoverEndpointModels(contract, baseUrl.trim(), apiKey || undefined);
			if (res.ok && res.models) {
				models = res.models;
				listOk = true;
				listResult = `Reached it. ${res.models.length} model${res.models.length === 1 ? '' : 's'} offered.`;
			} else {
				listOk = false;
				listResult = res.error ?? 'Could not reach that endpoint.';
			}
		} catch (e) {
			listOk = false;
			listResult = e instanceof Error ? e.message : 'Listing models failed';
		} finally {
			listing = false;
		}
	}

	async function save(payload: { baseUrl?: string; remove?: boolean }) {
		saving = true;
		error = '';
		saved = false;
		try {
			const body = buildSaveRequest(
				{ name, baseUrl, model, apiKey, defaultName },
				payload.remove === true
			);
			const accepted = await setEndpointSettings(contract, body);
			// The save is an intent: the 202 is a receipt, not a result. Wait for the
			// stored state to reach what the save reported, so a write the orchestrator
			// dropped is an error on screen rather than a page that looks configured.
			const applied = appliedTo(accepted, body.apiKey);
			settings = await pollUntil(fetchEndpointSettings.bind(null, contract), applied, SAVE_WINDOW);
			if (!applied(settings)) {
				throw new Error(`Saved, but the endpoint is still ${settings.baseUrl || 'unset'}. Check the logs.`);
			}
			// Re-seed the fields from what the server now holds rather than from what
			// was typed: the parser may have rewritten the address, and a Remove has
			// to empty the input instead of leaving the removed address on screen.
			name = settings.name || defaultName;
			baseUrl = settings.baseUrl;
			model = settings.model;
			apiKey = '';
			saved = true;
		} catch (e) {
			error = e instanceof Error ? e.message : `Could not save ${label.toLowerCase()} settings`;
		} finally {
			saving = false;
		}
	}

	function handleSave() {
		save({});
	}

	function handleRemove() {
		removing = true;
		save({ remove: true }).finally(() => (removing = false));
	}

	function useAIUpstream() {
		if (aiBaseUrl) {
			baseUrl = aiBaseUrl;
		}
	}
</script>

<div class="capability" data-contract={contract}>
	<h3 class="capability-title">{label}</h3>
	<p class="capability-description">{description}</p>

	{#if loading}
		<div class="loading-state"><p>Loading {label.toLowerCase()} settings...</p></div>
	{:else}
		<form class="capability-form" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
			<div class="form-field">
				<label for="{contract}-base-url">Base URL</label>
				<input
					id="{contract}-base-url"
					type="text"
					{placeholder}
					inputmode="url"
					autocomplete="off"
					autocapitalize="none"
					bind:value={baseUrl}
					disabled={saving}
					spellcheck="false"
				/>
				{#if aiBaseUrl && aiBaseUrl !== baseUrl}
					<button type="button" class="link-button" onclick={useAIUpstream} disabled={saving}>
						Use my AI upstream
					</button>
				{/if}
			</div>

			<div class="form-field">
				<label for="{contract}-key">
					API key
					{#if hasApiKey}<span class="pill pill-success">stored</span>{/if}
				</label>
				<!-- new-password, not off: blank means "keep the stored key", so the
				     last thing wanted is a manager autofilling the old one. -->
				<input
					id="{contract}-key"
					type="password"
					placeholder={hasApiKey ? 'unchanged' : 'sk-...'}
					bind:value={apiKey}
					disabled={saving}
					autocomplete="new-password"
				/>
				<span class="hint">
					{hasApiKey
						? 'Leave blank to keep the stored key.'
						: 'Stored in the secrets manager.'}
				</span>
			</div>

			<div class="form-row">
				<div class="form-field">
					<label for="{contract}-name">Name</label>
					<input id="{contract}-name" type="text" bind:value={name} disabled={saving} autocomplete="off" />
				</div>
				<div class="form-field">
					<label for="{contract}-model">Model</label>
					{#if models.length > 0}
						<select id="{contract}-model" bind:value={model} disabled={saving}>
							<option value="">(endpoint default)</option>
							{#each models as entry (entry)}
								<option value={entry}>{entry}</option>
							{/each}
						</select>
					{:else}
						<input
							id="{contract}-model"
							type="text"
							placeholder="endpoint default"
							bind:value={model}
							disabled={saving}
							autocomplete="off"
							spellcheck="false"
						/>
					{/if}
				</div>
			</div>

			<div class="actions">
				<Button variant="primary" type="submit" disabled={!dirty || saving}>
					{saving ? 'Applying…' : 'Save'}
				</Button>
				<Button
					variant="secondary"
					type="button"
					onclick={handleListModels}
					disabled={!baseUrl.trim() || listing}
				>
					{listing ? 'Listing…' : 'List models'}
				</Button>
				{#if configured}
					<Button variant="secondary" type="button" onclick={handleRemove} disabled={saving || removing}>
						{removing ? 'Removing…' : 'Remove'}
					</Button>
				{/if}
				{#if saved}
					<span class="saved-note">Saved. Apps pick this up on their next pass.</span>
				{/if}
			</div>

			{#if listResult}
				<!-- role=status: the listing takes a second and the result lands
				     wherever the pointer is, so it has to be announced. -->
				<p class="test-result" class:ok={listOk === true} class:fail={listOk === false} role="status">
					{listResult}
				</p>
			{/if}
		</form>
	{/if}

	{#if error}
		<div class="error-message" role="alert">{error}</div>
	{/if}

	{#if servedTo.length > 0}
		<div class="served-to">
			<h4>Served to</h4>
			<div class="served-list">
				{#each servedTo as consumer (consumer)}
					<div class="served-row">
						<span class="consumer-name">{consumer}</span>
					</div>
				{/each}
			</div>
		</div>
	{/if}
</div>

<style>
	/* No card and no rule between the capabilities: they are one group under the
	   AI heading, and the section spaces them with a gap. `min-width: 0` because a
	   flex item will not shrink below its content, and a long URL in a field would
	   otherwise set the page's minimum width. */
	.capability {
		min-width: 0;
	}

	.capability-title {
		margin: 0 0 var(--space-xs) 0;
		font-size: 1rem;
		font-weight: 500;
	}

	.capability-description {
		margin: 0 0 var(--space-lg) 0;
		color: var(--color-text-secondary);
		font-size: 0.875rem;
		line-height: 1.5;
	}

	.loading-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	.capability-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		min-width: 0;
	}

	.form-field label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.form-field input,
	.form-field select {
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
		transition: border-color 0.15s ease;
		width: 100%;
	}

	.form-field input:focus,
	.form-field select:focus {
		border-color: var(--color-accent);
		box-shadow: 0 0 0 3px rgba(28, 25, 23, 0.08);
	}

	/* `input` only: a `select` has no placeholder pseudo-element. */
	.form-field input::placeholder {
		color: var(--color-text-muted);
	}

	.form-field input:disabled,
	.form-field select:disabled {
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		cursor: not-allowed;
	}

	.form-row {
		display: flex;
		gap: var(--space-md);
		flex-wrap: wrap;
	}

	.form-row .form-field {
		flex: 1;
		/* min-width: 0, not a fixed floor: a flex item will not shrink below its
		   content, and the Model field carries a long placeholder, so a fixed
		   floor would set the page's minimum width and make Settings scroll
		   sideways at 390px. */
		min-width: 0;
	}

	.hint {
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	.link-button {
		align-self: flex-start;
		padding: 0;
		border: none;
		background: none;
		font-size: 0.75rem;
		color: var(--color-accent);
		text-decoration: underline;
		cursor: pointer;
	}

	.actions {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		flex-wrap: wrap;
	}

	.saved-note {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.test-result {
		margin: 0;
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		border-radius: var(--radius-md);
		border: 1px solid var(--color-border);
		background: var(--color-bg-subtle);
		color: var(--color-text-secondary);
	}

	.test-result.ok {
		color: var(--color-success);
		background: var(--color-success-bg);
		border-color: rgba(22, 101, 52, 0.15);
	}

	.test-result.fail {
		color: var(--color-error);
		background: var(--color-error-bg);
		border-color: rgba(153, 27, 27, 0.15);
	}

	.error-message {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: var(--color-error-bg);
		border: 1px solid rgba(153, 27, 27, 0.15);
		border-radius: var(--radius-md);
	}

	.pill {
		display: inline-block;
		margin-left: var(--space-xs);
		padding: 2px 8px;
		border-radius: 9999px;
		font-size: 0.6875rem;
		font-weight: 500;
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
		vertical-align: middle;
	}

	.pill-success {
		background: var(--color-success-bg);
		color: var(--color-success);
		border-color: rgba(22, 101, 52, 0.15);
	}

	.served-to {
		margin-top: var(--space-xl);
	}

	.served-to h4 {
		margin: 0 0 var(--space-md) 0;
		font-size: 0.875rem;
		font-weight: 500;
	}

	.served-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.served-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
	}

	.consumer-name {
		color: var(--color-text);
		font-weight: 500;
	}
</style>
