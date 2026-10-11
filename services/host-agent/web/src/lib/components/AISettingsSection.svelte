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

<section class="section ai-section">
	<h2>AI</h2>
	<p class="section-description">
		Point Bloud at an OpenAI-compatible server and every app that uses models
		follows. The key is stored in the secrets manager and is never read back
		by the browser. Apps adopt the default model only where they have not
		picked one themselves.
	</p>

	{#if loading}
		<div class="loading-state"><p>Loading AI settings...</p></div>
	{:else}
		<form class="ai-form" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
			<div class="form-field">
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

			<div class="form-field">
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
				<span class="hint">
					{hasApiKey
						? 'Leave blank to keep the stored key.'
						: 'Stored in the secrets manager, never read back by the browser.'}
				</span>
			</div>

			<div class="form-row">
				<div class="form-field">
					<label for="ai-name">Name</label>
					<input id="ai-name" type="text" bind:value={name} disabled={saving} autocomplete="off" />
				</div>
				<div class="form-field">
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
							placeholder="test the connection to list models"
							bind:value={defaultModel}
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
					onclick={handleTest}
					disabled={!baseUrl.trim() || testing}
				>
					{testing ? 'Testing…' : 'Test connection'}
				</Button>
				{#if saved}
					<span class="saved-note">Saved. Apps pick this up on their next pass.</span>
				{/if}
			</div>

			{#if testResult}
				<p class="test-result" class:ok={testOk === true} class:fail={testOk === false}>
					{testResult}
				</p>
			{/if}
		</form>
	{/if}

	{#if error}
		<div class="error-message">{error}</div>
	{/if}

	{#if servedTo.length > 0}
		<div class="served-to">
			<h3>Served to</h3>
			<div class="served-list">
				{#each servedTo as consumer (consumer.app)}
					<div class="served-row">
						<span class="consumer-name">{consumer.app}</span>
						<span
							class="pill"
							class:pill-info={consumer.via === 'gateway'}
							class:pill-error={consumer.via === 'none'}
						>
							{consumer.via}
						</span>
						{#if consumer.model}<span class="consumer-model">{consumer.model}</span>{/if}
					</div>
				{/each}
			</div>
		</div>
	{/if}
</section>

<style>
	/* The section shell matches the other settings sections (Address, Users)
	   exactly: same measure, same separator, same heading and description
	   type. This component has to carry its own rule -- the page's separator
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

	.ai-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	/* Field styling follows the settings page form pattern: serif inputs on the
	   elevated background, small semibold secondary label, accent border plus
	   soft ring on focus. */
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
	}

	.form-field input:focus,
	.form-field select:focus {
				border-color: var(--color-accent);
		box-shadow: 0 0 0 3px rgba(28, 25, 23, 0.08);
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
		min-width: 140px;
	}

	.hint {
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.saved-note {
		font-size: 0.875rem;
		color: var(--color-text-muted);
	}

	/* Test feedback uses the same tinted-panel shape as the page's error
	   message, so a result reads as a status rather than as stray text. */
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

	.pill-info {
		background: var(--color-info-bg);
		color: var(--color-info);
		border-color: rgba(12, 74, 110, 0.15);
	}

	.pill-error {
		background: var(--color-error-bg);
		color: var(--color-error);
		border-color: rgba(153, 27, 27, 0.15);
	}

	.served-to {
		margin-top: var(--space-2xl);
	}

	.served-to h3 {
		margin: 0 0 var(--space-md) 0;
		font-size: 0.9375rem;
		font-weight: 500;
	}

	/* Consumers render as rows in an elevated card, the way the Users list
	   does, rather than as a bare list. */
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

	/* The model id is an identifier the operator may need to copy verbatim, so
	   it stays monospaced the way the settings page renders other machine values. */
	.consumer-model {
		margin-left: auto;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}
</style>
