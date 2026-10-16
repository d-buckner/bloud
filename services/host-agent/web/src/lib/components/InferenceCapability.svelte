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

	interface Props {
		/**
		 * Reports the enabled chat base URL upward. The section hands it to the
		 * transcription and embeddings forms as a one-click fill, and this is the
		 * only component that knows when it changes.
		 */
		onbaseUrl?: (url: string) => void;
	}

	let { onbaseUrl }: Props = $props();

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

	// $effect rather than a call in onMount: the report has to re-run when a save
	// reloads the settings, not only once after the first fetch.
	$effect(() => {
		if (!settings) return;
		onbaseUrl?.(settings.upstreams.find((u) => u.enabled)?.baseUrl ?? '');
	});

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

<div class="capability">
	<h3 class="capability-title">Chat</h3>
	<p class="capability-description">
		An OpenAI-compatible endpoint for text generation. An app that summarizes,
		extracts, or answers follows what is set here, and runs without it if
		nothing is.
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
					bind:value={baseUrl}
					disabled={saving}
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
				<!-- role=status: the test takes a second and the result lands wherever
				     the pointer is, so it has to be announced rather than waited for. -->
				<p class="test-result" class:ok={testOk === true} class:fail={testOk === false} role="status">
					{testResult}
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
	.capability {
		margin-top: var(--space-2xl);
		padding-top: var(--space-xl);
		border-top: 1px solid var(--color-border);
	}

	.capability-title {
		margin: 0 0 var(--space-xs) 0;
		font-size: 1rem;
		font-weight: 500;
	}

	.capability-description {
		margin: 0 0 var(--space-lg) 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
		max-width: 60ch;
	}

	.ai-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.form-row {
		display: grid;
		grid-template-columns: 1fr 1.5fr;
		gap: var(--space-md);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		flex-wrap: wrap;
	}

	.saved-note {
		font-size: 0.8125rem;
		color: var(--color-success);
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
