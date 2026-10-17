<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Icon from './Icon.svelte';
	import { usePublicUrl } from '$lib/store';

	// The platform owns the store, the draft, the read state and the consent gate.
	// This component owns the markup.
	const url = usePublicUrl();
</script>

<section class="section address-section">
	<h2>Host</h2>
	<p class="section-description">
		Where you reach this Bloud from outside. The scheme and the port belong
		to the proxy: set them to whatever answers there, not to what Bloud
		listens on internally.
	</p>

	{#if url.loading}
		<div class="loading-state"><p>Loading address...</p></div>
	{:else}
		<!-- The consequence used to be the last sentence of the paragraph above,
		     in the same type and colour as the explanation, and a user could save
		     without ever meeting it. Saving this field restarts every app on the
		     instance, so it is now a callout that only appears when the value has
		     actually changed, states the blast radius, and has to be acknowledged
		     before Save works. The input is described by it, so the warning is read
		     with the field rather than hoping the user scrolled past it. -->
		{#if url.dirty}
			<aside class="callout" aria-labelledby="address-warning-title">
				<span class="callout-icon" aria-hidden="true">
					<Icon name="warning" size={16} />
				</span>
				<div class="callout-body">
					<strong id="address-warning-title">This restarts your apps</strong>
					<p>
						A new address means new SSO URLs. Bloud re-registers the redirect URIs
						and the issuer baked into every app, and restarts each one so they pick
						the new values up. Expect a few seconds per app, in dependency order.
						Nothing is deleted and nobody is signed out.
					</p>
					<label class="acknowledge">
						<input type="checkbox" bind:checked={url.acknowledged} />
						I understand my apps will briefly restart.
					</label>
				</div>
			</aside>
		{/if}

		<form class="address-form" onsubmit={(e) => { e.preventDefault(); url.save(); }}>
			<input
				type="text"
				class="address-input"
				placeholder="https://bloud.example.com"
				aria-label="Public address"
				aria-describedby={url.dirty ? 'address-warning-title' : undefined}
				inputmode="url"
				autocomplete="off"
				autocapitalize="none"
				spellcheck="false"
				required
				bind:value={url.edit}
			/>
			<button
				class="btn btn-primary"
				type="submit"
				disabled={!url.dirty || !url.edit.trim() || url.blocked}
			>
				Save
			</button>
		</form>
	{/if}

	{#if url.error}
		<div class="error-message" role="alert">{url.error}</div>
	{/if}
</section>

<style>
	/* The section shell (measure, heading) comes from `base.css`, where every
	   settings section reads the same rule. Address is the first section, so it
	   has no separator above it. */
	.section-description {
		margin: 0 0 var(--space-md) 0;
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		line-height: 1.5;
	}

	.loading-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	.address-form {
		display: flex;
		gap: var(--space-sm);
		max-width: 32rem;
	}

	/* The callout. Amber fill, a rule of its own, and separated from the prose so
	   it cannot be read as another sentence of the explanation. */
	.callout {
		display: flex;
		gap: var(--space-sm);
		align-items: flex-start;
		margin-bottom: var(--space-md);
		padding: var(--space-md);
		background: var(--color-warning-bg);
		border: 1px solid var(--color-warning);
		border-left-width: 3px;
		border-radius: var(--radius-md);
		color: var(--color-text);
	}

	.callout-icon {
		color: var(--color-warning);
		display: inline-flex;
		margin-top: 2px;
	}

	.callout-body {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		min-width: 0;
	}

	.callout-body p {
		margin: 0;
		font-size: 0.875rem;
		line-height: 1.5;
		color: var(--color-text-secondary);
	}

	.acknowledge {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-top: var(--space-xs);
		font-size: 0.875rem;
		cursor: pointer;
	}

	.acknowledge input {
		width: 18px;
		height: 18px;
		accent-color: var(--color-warning);
	}

	.address-input {
		flex: 1;
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-mono);
		font-size: 0.875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.address-input:focus {
				border-color: var(--color-accent);
	}

	.address-input::placeholder {
		color: var(--color-text-muted);
	}

	.address-input:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn {
		padding: var(--space-sm) var(--space-lg);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		cursor: pointer;
		transition: all 0.15s ease;
		align-self: flex-start;
	}

	/* Matches Button.svelte's disabled treatment: the absence of a fill, not the
	   accent at 60% opacity (which composites to a mid-grey and reads as an
	   enabled grey button). */
	.btn:disabled {
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		border-color: var(--color-border);
		cursor: not-allowed;
		opacity: 1;
	}

	.btn-primary {
		background: var(--color-accent);
		color: var(--color-on-accent);
	}

	.btn-primary:hover:not(:disabled) {
		opacity: 0.9;
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

	/* The address row is an input and a button side by side, which is 32rem of
	   intent in a 390px viewport. Below the width where they fit they stack, with
	   the button stretched so it stays a thumb-sized target. */
	@media (max-width: 480px) {
		.address-form {
			flex-direction: column;
			align-items: stretch;
		}

		.address-form .btn {
			align-self: stretch;
		}
	}
</style>
