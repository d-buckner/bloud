<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import {
		discoverTranscriptionModels,
		fetchAISettings,
		fetchTranscriptionSettings,
		setTranscriptionSettings,
		type TranscriptionSettings
	} from '$lib/clients/settingsClient';
	import { appliedTo, buildSaveRequest, SAVE_WINDOW } from '$lib/utils/transcriptionSave';
	import Button from '$lib/components/Button.svelte';
	import { pollUntil } from '$lib/store/effects/convergence';

	let settings = $state<TranscriptionSettings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	let removing = $state(false);
	let error = $state('');
	let saved = $state(false);

	let name = $state('Transcription');
	let baseUrl = $state('');
	let apiKey = $state('');
	let model = $state('');
	let models = $state<string[]>([]);
	let listing = $state(false);
	let listResult = $state('');
	let listOk = $state<boolean | null>(null);

	// The AI upstream address, read only to offer "same server" as a click. It
	// is a convenience, not a dependency: if the AI settings fail to load the
	// operator still types the address themselves.
	let aiBaseUrl = $state('');

	let dirty = $derived(
		name !== (settings?.name ?? 'Transcription') ||
			baseUrl !== (settings?.baseUrl ?? '') ||
			model !== (settings?.model ?? '') ||
			apiKey !== ''
	);
	let configured = $derived(settings?.configured ?? false);
	let hasApiKey = $derived(settings?.hasApiKey ?? false);
	let servedTo = $derived(settings?.servedTo ?? []);

	onMount(async () => {
		try {
			settings = await fetchTranscriptionSettings();
			name = settings.name || 'Transcription';
			baseUrl = settings.baseUrl;
			model = settings.model;
			// The stored list is not returned (there is none: the endpoint owns
			// its models), so the picker starts empty until the operator lists.
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not load transcription settings';
		} finally {
			loading = false;
		}
		// Second, and never fatal: a missing AI upstream only means the
		// convenience button does not appear.
		try {
			const ai = await fetchAISettings();
			aiBaseUrl = ai.upstreams.find((u) => u.enabled)?.baseUrl ?? '';
		} catch {
			aiBaseUrl = '';
		}
	});

	async function handleListModels() {
		if (!baseUrl.trim()) return;
		listing = true;
		listResult = '';
		listOk = null;
		try {
			const res = await discoverTranscriptionModels(baseUrl.trim(), apiKey || undefined);
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
			const body = buildSaveRequest({ name, baseUrl, model, apiKey }, payload.remove === true);
			const accepted = await setTranscriptionSettings(body);
			// The save is an intent: the 202 is a receipt, not a result. Wait for the
			// stored state to reach what the save reported, so a write the orchestrator
			// dropped is an error on screen rather than a page that looks configured.
			const applied = appliedTo(accepted, body.apiKey);
			settings = await pollUntil(fetchTranscriptionSettings, applied, SAVE_WINDOW);
			if (!applied(settings)) {
				throw new Error(`Saved, but the endpoint is still ${settings.baseUrl || 'unset'}. Check the logs.`);
			}
			// Re-seed the fields from what the server now holds rather than from what
			// was typed: the parser may have rewritten the address, and a Remove has
			// to empty the input instead of leaving the removed address on screen.
			name = settings.name || 'Transcription';
			baseUrl = settings.baseUrl;
			model = settings.model;
			apiKey = '';
			saved = true;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not save transcription settings';
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

<section class="section transcription-section">
	<h2>Transcription</h2>
	<p class="section-description">
		An OpenAI-compatible endpoint that turns audio into timed text. This is a
		separate setting from AI on purpose: a server that answers chat can be
		the same box or a different one, and Bloud never assumes the address that
		completes also transcribes. Apps that transcribe follow what is set here,
		and run without it if nothing is.
	</p>

	{#if loading}
		<div class="loading-state"><p>Loading transcription settings...</p></div>
	{:else}
		<form class="transcription-form" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
			<div class="form-field">
				<label for="tr-base-url">Base URL</label>
				<input
					id="tr-base-url"
					type="text"
					placeholder="https://api.openai.com/v1"
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
				<label for="tr-key">
					API key
					{#if hasApiKey}<span class="pill pill-success">stored</span>{/if}
				</label>
				<!-- new-password, not off: blank means "keep the stored key", so the
				     last thing wanted is a manager autofilling the old one. -->
				<input
					id="tr-key"
					type="password"
					placeholder={hasApiKey ? 'unchanged' : 'sk-...'}
					bind:value={apiKey}
					disabled={saving}
					autocomplete="new-password"
				/>
				<span class="hint">
					{hasApiKey
						? 'Leave blank to keep the stored key.'
						: 'Stored in the secrets manager, never read back by the browser.'}
				</span>
			</div>

			<div class="form-row">
				<div class="form-field">
					<label for="tr-name">Name</label>
					<input id="tr-name" type="text" bind:value={name} disabled={saving} autocomplete="off" />
				</div>
				<div class="form-field">
					<label for="tr-model">Model</label>
					{#if models.length > 0}
						<select id="tr-model" bind:value={model} disabled={saving}>
							<option value="">(endpoint default)</option>
							{#each models as entry (entry)}
								<option value={entry}>{entry}</option>
							{/each}
						</select>
					{:else}
						<input
							id="tr-model"
							type="text"
							placeholder="blank uses the endpoint default"
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
			<h3>Served to</h3>
			<div class="served-list">
				{#each servedTo as consumer (consumer)}
					<div class="served-row">
						<span class="consumer-name">{consumer}</span>
					</div>
				{/each}
			</div>
		</div>
	{/if}
</section>

<style>
	/* The section shell matches the other settings sections (Address, Users,
	   AI) exactly: same measure, same separator, same heading and description
	   type. This component carries its own rule because the page's separator
	   selector does not reach into a child component's scoped styles. */
	.section {
		max-width: 560px;
		margin-top: var(--space-2xl);
		padding-top: var(--space-2xl);
		border-top: 1px solid var(--color-border);
	}

	.section h2 {
		margin: 0 0 var(--space-xs) 0;
		font-size: 1.125rem;
		font-weight: 500;
	}

	.section-description {
		margin: 0 0 var(--space-xl) 0;
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		line-height: 1.5;
	}

	.loading-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	.transcription-form {
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
		max-width: 100%;
	}

	.form-field input:focus,
	.form-field select:focus {
		border-color: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-text) 12%, transparent);
	}

	.form-field input::placeholder,
	.form-field select::placeholder {
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
		border-color: color-mix(in srgb, var(--color-success) 30%, transparent);
	}

	.test-result.fail {
		color: var(--color-error);
		background: var(--color-error-bg);
		border-color: color-mix(in srgb, var(--color-error) 30%, transparent);
	}

	.error-message {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: var(--color-error-bg);
		border: 1px solid color-mix(in srgb, var(--color-error) 30%, transparent);
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
		border-color: color-mix(in srgb, var(--color-success) 30%, transparent);
	}

	.served-to {
		margin-top: var(--space-2xl);
	}

	.served-to h3 {
		margin: 0 0 var(--space-md) 0;
		font-size: 0.9375rem;
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
