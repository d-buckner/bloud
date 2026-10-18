<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { useAI } from '$lib/store';
	import Button from '$lib/components/Button.svelte';

	// The platform owns the settings, the fields, the probe and the save. This
	// component owns the markup, and the fill it offers to the other forms is read
	// by their hooks instead of reported upward from here.
	const ai = useAI();
</script>

<div class="capability">
	<h3 class="capability-title">Chat</h3>
	<p class="capability-description">
		Text generation
	</p>

	{#if ai.loading}
		<div class="loading-state"><p>Loading AI settings...</p></div>
	{:else}
		<form class="ai-form" onsubmit={(e) => { e.preventDefault(); ai.save(); }}>
			<div class="form-field">
				<label for="ai-base-url">Base URL</label>
				<input
					id="ai-base-url"
					type="text"
					placeholder="https://api.example.com/v1"
					inputmode="url"
					autocomplete="off"
					autocapitalize="none"
					bind:value={ai.baseUrl.value}
					spellcheck="false"
				/>
			</div>

			<div class="form-field">
				<label for="ai-key">
					API key
					{#if ai.hasApiKey}<span class="pill pill-success">stored</span>{/if}
				</label>
				<!-- new-password, not off: blank means "keep the stored key", so the
				     last thing wanted is a manager autofilling the old one. -->
				<input
					id="ai-key"
					type="password"
					placeholder={ai.hasApiKey ? 'unchanged' : 'sk-...'}
					bind:value={ai.apiKey.value}
					autocomplete="new-password"
				/>
				<span class="hint">
					{ai.hasApiKey
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
						bind:value={ai.name.value}
						autocomplete="off"
					/>
				</div>
				<div class="form-field">
					<label for="ai-model">Default model</label>
					{#if ai.models.length > 0}
						<select
							id="ai-model"
							bind:value={ai.defaultModel.value}
						>
							<option value="">(none)</option>
							{#each ai.models as model (model)}
								<option value={model}>{model}</option>
							{/each}
						</select>
					{:else}
						<input
							id="ai-model"
							type="text"
							placeholder="test the connection to list models"
							bind:value={ai.defaultModel.value}
									autocomplete="off"
							spellcheck="false"
						/>
					{/if}
				</div>
			</div>

			<div class="actions">
				<Button variant="primary" type="submit" disabled={!ai.dirty}>Save</Button>
				<Button
					variant="secondary"
					type="button"
					onclick={() => ai.test()}
					disabled={!ai.baseUrl.value.trim() || ai.testing}
				>
					{ai.testing ? 'Testing…' : 'Test connection'}
				</Button>
			</div>

			{#if ai.testResult}
				<!-- role=status: the test takes a second and the result lands wherever
				     the pointer is, so it has to be announced rather than waited for. -->
				<p class="test-result" class:ok={ai.testOk === true} class:fail={ai.testOk === false} role="status">
					{ai.testResult}
				</p>
			{/if}
		</form>
	{/if}

	{#if ai.error}
		<div class="error-message" role="alert">{ai.error}</div>
	{/if}

	{#if ai.servedTo.length > 0}
		<div class="served-to">
			<h3>Served to</h3>
			<div class="served-list">
				{#each ai.servedTo as consumer (consumer.app)}
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
