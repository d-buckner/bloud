<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { testAIEndpoint, type AIUpstream } from '$lib/clients/settingsClient';
	import { aiView, commands, select } from '$lib/store';
	import { draft, sameText } from '$lib/store/draft.svelte';
	import Button from '$lib/components/Button.svelte';

	// No props. The chat base URL used to travel upward through a callback, which
	// meant the parent kept a copy and an effect kept the copy current. The parent
	// selects `aiView` instead, because the value is store shown.
	const view = select(aiView);
	const shown = $derived(view.current);

	// The form holds one upstream (v1 is single-upstream; the shape is a list so
	// adding a second later is not a redesign). Each edit falls back to the live
	// value, so a save landing elsewhere cannot overwrite a field being typed.
	const name = draft(() => shown.value?.upstreams[0]?.name || 'Default', sameText);
	const baseUrl = draft(() => shown.value?.upstreams[0]?.baseUrl ?? '', sameText);
	const defaultModel = draft(() => shown.value?.defaultModel ?? '', sameText);
	// The key is write-only: the API never returns it, so blank means "keep".
	let apiKey = $state('');

	const hasApiKey = $derived(shown.value?.hasApiKey ?? false);
	const servedTo = $derived(shown.confirmed?.servedTo ?? []);
	const loading = $derived(
		shown.phase === 'idle' ||
			(shown.phase === 'loading' && shown.value === null)
	);
	const loadError = $derived(shown.phase === 'error' ? shown.error : '');

	// The probe's answer, which is this form's own: it lists the models a
	// candidate endpoint offers, and nothing else reads it.
	let models = $state<string[]>([]);
	let testing = $state(false);
	let testResult = $state('');
	let testOk = $state<boolean | null>(null);

	// The stored list is the fallback, so a configured endpoint shows its models
	// before anyone runs a test.
	const modelOptions = $derived(
		models.length > 0 ? models : (shown.value?.upstreams[0]?.models ?? [])
	);

	// Dirty against the value the user sees, so the claim makes the form clean and
	// Save disables itself for the whole wait.
	const dirty = $derived(
		name.dirty || baseUrl.dirty || defaultModel.dirty || apiKey !== ''
	);

	onMount(() => {
		void commands.loadAISettings();
	});

	async function handleTest() {
		if (!baseUrl.value.trim()) return;
		testing = true;
		testResult = '';
		testOk = null;
		try {
			const res = await testAIEndpoint(baseUrl.value.trim(), apiKey || undefined);
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

	function clearFields(): void {
		name.clear();
		baseUrl.clear();
		defaultModel.clear();
		apiKey = '';
	}

	/**
	 * The store owns the claim, the reload and the wait. Clearing the edits at the
	 * moment of submit hands the fields to the claim, so the typed values stay on
	 * screen, the stored ones replace them, and a rejected save raises a toast and
	 * falls back to the confirmed settings.
	 */
	function handleSave(): void {
		const upstream: AIUpstream = {
			id: shown.value?.upstreams[0]?.id ?? 'default',
			name: name.value.trim() || 'Default',
			baseUrl: baseUrl.value.trim(),
			models: modelOptions,
			enabled: true
		};
		const payload: Parameters<typeof commands.saveAISettings>[0] = {
			upstreams: baseUrl.value.trim() ? [upstream] : [],
			defaultModel: defaultModel.value.trim()
		};
		// Only include the key when it was typed. Omitting means "keep".
		if (apiKey !== '') payload.apiKey = apiKey;

		void commands.saveAISettings(payload);
		clearFields();
	}
</script>

<div class="capability">
	<h3 class="capability-title">Chat</h3>
	<p class="capability-description">
		Text generation
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
					inputmode="url"
					autocomplete="off"
					autocapitalize="none"
					value={baseUrl.value}
					oninput={(e) => baseUrl.set(e.currentTarget.value)}
					spellcheck="false"
				/>
			</div>

			<div class="form-field">
				<label for="ai-key">
					API key
					{#if hasApiKey}<span class="pill pill-success">stored</span>{/if}
				</label>
				<!-- new-password, not off: blank means "keep the stored key", so the
				     last thing wanted is a manager autofilling the old one. -->
				<input
					id="ai-key"
					type="password"
					placeholder={hasApiKey ? 'unchanged' : 'sk-...'}
					value={apiKey}
					oninput={(e) => (apiKey = e.currentTarget.value)}
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
					<label for="ai-name">Name</label>
					<input
						id="ai-name"
						type="text"
						value={name.value}
						oninput={(e) => name.set(e.currentTarget.value)}
						autocomplete="off"
					/>
				</div>
				<div class="form-field">
					<label for="ai-model">Default model</label>
					{#if modelOptions.length > 0}
						<select
							id="ai-model"
							value={defaultModel.value}
							onchange={(e) => defaultModel.set(e.currentTarget.value)}
						>
							<option value="">(none)</option>
							{#each modelOptions as model (model)}
								<option value={model}>{model}</option>
							{/each}
						</select>
					{:else}
						<input
							id="ai-model"
							type="text"
							placeholder="test the connection to list models"
							value={defaultModel.value}
							oninput={(e) => defaultModel.set(e.currentTarget.value)}
									autocomplete="off"
							spellcheck="false"
						/>
					{/if}
				</div>
			</div>

			<div class="actions">
				<Button variant="primary" type="submit" disabled={!dirty}>Save</Button>
				<Button
					variant="secondary"
					type="button"
					onclick={handleTest}
					disabled={!baseUrl.value.trim() || testing}
				>
					{testing ? 'Testing…' : 'Test connection'}
				</Button>
			</div>

			{#if testResult}
				<!-- role=status: the test takes a second and the result lands wherever
				     the pointer is, so it has to be announced rather than waited for. -->
				<p class="test-result" class:ok={testOk === true} class:fail={testOk === false} role="status">
					{testResult}
				</p>
			{/if}
		</form>
	{/if}

	{#if loadError}
		<div class="error-message" role="alert">{loadError}</div>
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
		margin: 0 0 var(--space-md) 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
		max-width: 60ch;
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

	/* The field styles are the ones `EndpointCapability` already carries, and they
	   have to be repeated here rather than shared: Svelte scopes a component's
	   CSS to that component, so a class with no rule in this file renders as a
	   browser default. That is what this file was: an unstyled text input with a
	   `2px inset` border sitting next to the themed ones two sections down. */
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
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-text) 12%, transparent);
	}

	/* `input` only: a `select` has no placeholder pseudo-element, so the paired
	   selector matched nothing. */
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
		display: grid;
		grid-template-columns: 1fr 1.5fr;
		gap: var(--space-md);
	}

	.hint {
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-muted);
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

	/* The two `Served to` states that are neither good nor broken (gateway) and
	   neither good nor broken-but-absent (none). No component defined these
	   before, so the markup carried classes nothing styled. */
	.pill-info {
		background: var(--color-info-bg);
		color: var(--color-info);
		border-color: color-mix(in srgb, var(--color-info) 30%, transparent);
	}

	.pill-error {
		background: var(--color-error-bg);
		color: var(--color-error);
		border-color: color-mix(in srgb, var(--color-error) 30%, transparent);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		flex-wrap: wrap;
	}


	.test-result {
		margin: 0;
		font-size: 0.8125rem;
	}

	.test-result.ok {
		color: var(--color-success);
	}

	.test-result.fail {
		color: var(--color-error);
	}

	.served-to {
		margin-top: var(--space-lg);
	}

	.served-to h3 {
		margin: 0 0 var(--space-sm) 0;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.served-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.served-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 0.8125rem;
	}

	.consumer-name {
		min-width: 8rem;
	}

	.consumer-model {
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}
</style>
