<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { untrack } from 'svelte';
	import { useEndpoint } from '$lib/store';
	import Button from '$lib/components/Button.svelte';
	import type { EndpointContract } from '$lib/clients/settingsClient';

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
	}

	let { contract, label, description, defaultName, placeholder }: Props = $props();

	// The platform owns the endpoint, its fields, the probe, the save and the chat
	// address offered as a fill. This component owns the markup.
	//
	// A contract and a default name are this component's identity, not reactive
	// values: one instance serves one contract for its whole life. `untrack` says
	// that on purpose instead of leaving a warning to guess at.
	const ep = useEndpoint(untrack(() => contract), untrack(() => defaultName));
</script>

<div class="capability" data-contract={contract}>
	<h3 class="capability-title">{label}</h3>
	<p class="capability-description">{description}</p>

	{#if ep.loading}
		<div class="loading-state"><p>Loading {label.toLowerCase()} settings...</p></div>
	{:else}
		<form class="capability-form" onsubmit={(e) => { e.preventDefault(); ep.save(); }}>
			<div class="form-field">
				<label for="{contract}-base-url">Base URL</label>
				<input
					id="{contract}-base-url"
					type="text"
					{placeholder}
					inputmode="url"
					autocomplete="off"
					autocapitalize="none"
					bind:value={ep.baseUrl.edit}
					spellcheck="false"
				/>
				{#if ep.canUseAIUpstream}
					<button type="button" class="link-button" onclick={() => ep.useAIUpstream()}>
						Use my AI upstream
					</button>
				{/if}
			</div>

			<div class="form-field">
				<label for="{contract}-key">
					API key
					{#if ep.hasApiKey}<span class="pill pill-success">stored</span>{/if}
				</label>
				<!-- new-password, not off: blank means "keep the stored key", so the
				     last thing wanted is a manager autofilling the old one. -->
				<input
					id="{contract}-key"
					type="password"
					placeholder={ep.hasApiKey ? 'unchanged' : 'sk-...'}
					bind:value={ep.apiKey.edit}
					autocomplete="new-password"
				/>
				<span class="hint">
					{ep.hasApiKey
						? 'Leave blank to keep the stored key.'
						: 'Stored in the secrets manager.'}
				</span>
			</div>

			<div class="form-row">
				<div class="form-field">
					<label for="{contract}-name">Name</label>
					<input
						id="{contract}-name"
						type="text"
						bind:value={ep.name.edit}
						autocomplete="off"
					/>
				</div>
				<div class="form-field">
					<label for="{contract}-model">Model</label>
					{#if ep.models.length > 0}
						<select
						id="{contract}-model"
						bind:value={ep.model.edit}
					>
							<option value="">(endpoint default)</option>
							{#each ep.models as entry (entry)}
								<option value={entry}>{entry}</option>
							{/each}
						</select>
					{:else}
						<input
							id="{contract}-model"
							type="text"
							placeholder="endpoint default"
							bind:value={ep.model.edit}
							autocomplete="off"
							spellcheck="false"
						/>
					{/if}
				</div>
			</div>

			<div class="actions">
				<Button variant="primary" type="submit" disabled={!ep.dirty}>Save</Button>
				<Button
					variant="secondary"
					type="button"
					onclick={() => ep.test()}
					disabled={!ep.baseUrl.edit.trim() || ep.testing}
				>
					{ep.testing ? 'Listing…' : 'List models'}
				</Button>
				{#if ep.configured}
					<Button variant="secondary" type="button" onclick={() => ep.remove()}>Remove</Button>
				{/if}
			</div>

			{#if ep.testResult}
				<!-- role=status: the listing takes a second and the result lands
				     wherever the pointer is, so it has to be announced. -->
				<p class="test-result" class:ok={ep.testOk === true} class:fail={ep.testOk === false} role="status">
					{ep.testResult}
				</p>
			{/if}
		</form>
	{/if}

	{#if ep.error}
		<div class="error-message" role="alert">{ep.error}</div>
	{/if}

	{#if ep.servedTo.length > 0}
		<div class="served-to">
			<h4>Served to</h4>
			<div class="served-list">
				{#each ep.servedTo as consumer (consumer)}
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
		margin: 0 0 var(--space-md) 0;
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
		gap: var(--space-md);
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
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-text) 12%, transparent);
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
