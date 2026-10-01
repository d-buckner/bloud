<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import {
		fetchAISettings,
		setAISettings,
		testAIEndpoint,
		type AISettings,
		type AIUpstream
	} from '$lib/clients/settingsClient';
	import Button from '$lib/components/Button.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let settings = $state<AISettings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	let error = $state('');
	let saved = $state(false);

	// The form holds one upstream (v1 is single-upstream; the shape is a list so
	// adding a second later is not a redesign). The API key is held separately
	// and only sent when the operator typed one, so a save never blanks a stored
	// credential by omission.
	let name = $state('Default');
	let baseUrl = $state('');
	let apiKey = $state('');
	let defaultModel = $state('');
	let models = $state<string[]>([]);
	let testing = $state(false);
	let testResult = $state('');
	let testOk = $state<boolean | null>(null);

	let dirty = $derived(
		name !== (settings?.upstreams[0]?.name ?? 'Default') ||
			baseUrl !== (settings?.upstreams[0]?.baseUrl ?? '') ||
			defaultModel !== (settings?.defaultModel ?? '') ||
			apiKey !== ''
	);

	// Derived rather than read off `settings` in the markup: the `{#if loading}`
	// branch does not narrow the reactive state for TypeScript, so a direct
	// `settings.hasApiKey` inside it fails the typecheck.
	let hasApiKey = $derived(settings?.hasApiKey ?? false);
	let servedTo = $derived(settings?.servedTo ?? []);

	onMount(async () => {
		try {
			settings = await fetchAISettings();
			const upstream = settings.upstreams[0];
			if (upstream) {
				name = upstream.name;
				baseUrl = upstream.baseUrl;
				models = upstream.models ?? [];
			}
			defaultModel = settings.defaultModel;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not load AI settings';
		} finally {
			loading = false;
		}
	});

	async function handleTest() {
		if (!baseUrl.trim()) return;
		testing = true;
		testResult = '';
		testOk = null;
		try {
			const res = await testAIEndpoint(baseUrl.trim(), apiKey || undefined);
			if (res.ok && res.models) {
				models = res.models;
				testOk = true;
				testResult = `Reached it. ${res.models.length} model${res.models.length === 1 ? '' : 's'} available.`;
			} else {
				testOk = false;
				testResult = res.error ?? 'Could not reach that endpoint.';
			}
		} catch (e) {
			testOk = false;
			testResult = e instanceof Error ? e.message : 'Test failed';
		} finally {
			testing = false;
		}
	}

	async function handleSave() {
		saving = true;
		error = '';
		saved = false;
		try {
			const upstream: AIUpstream = {
				id: settings?.upstreams[0]?.id ?? 'default',
				name: name.trim() || 'Default',
				baseUrl: baseUrl.trim(),
				models,
				enabled: true
			};
			const payload: Parameters<typeof setAISettings>[0] = {
				upstreams: baseUrl.trim() ? [upstream] : [],
				defaultModel: defaultModel.trim()
			};
			// Only include the key when it was typed. Omitting means "keep".
			if (apiKey !== '') {
				payload.apiKey = apiKey;
			}
			await setAISettings(payload);
			saved = true;
			apiKey = '';
			settings = await fetchAISettings();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Could not save AI settings';
		} finally {
			saving = false;
		}
	}
</script>

<section class="section">
	<h2>AI</h2>
	<p class="section-description">
		Point Bloud at an OpenAI-compatible server and every app that uses models
		follows. The key is stored in the secrets manager and is never read back
		by the browser. Apps adopt the default model only where they have not
		picked one themselves; installing a gateway app like LiteLLM takes over
		from the instance setting automatically.
	</p>

	{#if loading}
		<div class="loading-state">Loading AI settings…</div>
	{:else}
		<form class="ai-form" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
			<div class="field-row">
				<div class="field">
					<label for="ai-name">Name</label>
					<input id="ai-name" type="text" bind:value={name} disabled={saving} autocomplete="off" />
				</div>
				<div class="field">
					<label for="ai-base-url">Base URL</label>
					<input
						id="ai-base-url"
						type="text"
						placeholder="https://api.example.com/v1"
						bind:value={baseUrl}
						disabled={saving}
						autocomplete="off"
						spellcheck="false"
					/>
				</div>
			</div>

			<div class="field-row">
				<div class="field">
					<label for="ai-key">
						API key
						{#if hasApiKey}<span class="pill pill-success">stored</span>{/if}
					</label>
					<input
						id="ai-key"
						type="password"
						placeholder={hasApiKey ? 'unchanged' : 'sk-...'}
						bind:value={apiKey}
						disabled={saving}
						autocomplete="off"
					/>
				</div>
				<div class="field field-action">
					<span class="label-spacer" aria-hidden="true"></span>
					<Button
						variant="secondary"
						type="button"
						onclick={handleTest}
						disabled={!baseUrl.trim() || testing}
					>
						{testing ? 'Testing…' : 'Test connection'}
					</Button>
				</div>
			</div>

			{#if testResult}
				<p class="test-result" class:ok={testOk === true} class:fail={testOk === false}>
					{testResult}
				</p>
			{/if}

			<div class="field">
				<label for="ai-model">Default model</label>
				{#if models.length > 0}
					<select id="ai-model" bind:value={defaultModel} disabled={saving}>
						<option value="">(none)</option>
						{#each models as model (model)}
							<option value={model}>{model}</option>
						{/each}
					</select>
				{:else}
					<input
						id="ai-model"
						type="text"
						placeholder="test the connection to pick from the live list"
						bind:value={defaultModel}
						disabled={saving}
						autocomplete="off"
						spellcheck="false"
					/>
				{/if}
			</div>

			<div class="actions">
				<Button variant="primary" type="submit" disabled={!dirty || saving}>
					{saving ? 'Applying…' : 'Save'}
				</Button>
				{#if saved}
					<span class="saved-note">Saved. Apps pick this up on their next pass.</span>
				{/if}
			</div>
		</form>
	{/if}

	{#if error}
		<div class="error-box"><ErrorState message={error} /></div>
	{/if}

	{#if servedTo.length > 0}
		<div class="served-to">
			<h3>Served to</h3>
			<ul>
				{#each servedTo as consumer (consumer.app)}
					<li>
						<span class="consumer-name">{consumer.app}</span>
						<span class="pill" class:pill-info={consumer.via === 'gateway'} class:pill-error={consumer.via === 'none'}>
							{consumer.via}
						</span>
						{#if consumer.model}<span class="consumer-model">{consumer.model}</span>{/if}
					</li>
				{/each}
			</ul>
		</div>
	{/if}
</section>

<style>
	.section {
		max-width: 560px;
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

	.ai-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.field-row {
		display: flex;
		gap: var(--space-md);
		align-items: flex-start;
	}

	/* Field styling follows the house form pattern (ShareModal): serif inputs on
	   the base background, muted label, accent border on focus. */
	.field {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		flex: 1;
		min-width: 0;
	}

	.field label {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.field input,
	.field select {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		font-size: 1rem;
		font-family: var(--font-serif);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.field input:focus,
	.field select:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.field input:disabled,
	.field select:disabled {
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
	}

	.field input::placeholder,
	.field select::placeholder {
		color: var(--color-text-muted);
	}

	.field-action {
		flex: 0 0 auto;
	}

	/* Keeps the button baseline aligned with the input in the row beside it,
	   since this field has no visible label of its own. */
	.label-spacer {
		display: block;
		height: calc(0.875rem + var(--space-xs));
	}

	.test-result {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	.test-result.ok {
		color: var(--color-success);
	}

	.test-result.fail {
		color: var(--color-error);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		margin-top: var(--space-sm);
	}

	.saved-note {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	.error-box {
		margin-top: var(--space-md);
		padding: var(--space-md);
		background: var(--color-error-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.pill {
		display: inline-block;
		margin-left: var(--space-xs);
		padding: 1px var(--space-sm);
		border-radius: 999px;
		font-size: 0.6875rem;
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
		vertical-align: middle;
	}

	.pill-success {
		background: var(--color-success-bg);
		color: var(--color-success);
	}

	.pill-info {
		background: var(--color-info-bg);
		color: var(--color-info);
	}

	.pill-error {
		background: var(--color-error-bg);
		color: var(--color-error);
	}

	.served-to {
		margin-top: var(--space-xl);
	}

	.served-to h3 {
		margin: 0 0 var(--space-sm) 0;
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.served-to ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.served-to li {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 0.9375rem;
	}

	.consumer-name {
		color: var(--color-text);
	}

	/* The model id is an identifier the operator may need to copy verbatim, so
	   it stays monospaced the way the settings page renders other machine values. */
	.consumer-model {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}
</style>
