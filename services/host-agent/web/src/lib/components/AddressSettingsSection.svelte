<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import Icon from './Icon.svelte';
	import { fetchPublicURL, setPublicURL } from '$lib/clients/settingsClient';

	// Address state. One URL in, one URL out: the scheme, the host, and the
	// port the proxy is dialed on all live in the string the operator types,
	// so there is nothing else to hold.
	let savedUrl = $state('');
	let draftUrl = $state('');
	let loading = $state(true);
	let error = $state('');
	let saving = $state(false);
	// The acknowledgement for a changed address. It is a real gate on Save, not a
	// decoration: see the callout in the markup for why.
	let acknowledged = $state(false);

	const addressDirty = $derived(draftUrl.trim() !== savedUrl);
	const saveBlocked = $derived(addressDirty && !acknowledged);

	// Any edit to the draft withdraws the acknowledgement. A user who agreed to
	// restart the apps on one address has not agreed to whatever they typed next.
	$effect(() => {
		draftUrl;
		acknowledged = false;
	});

	const POLL_INTERVAL = 500;
	const APPLY_TIMEOUT = 20_000;

	onMount(loadAddress);

	async function loadAddress() {
		loading = true;
		error = '';
		try {
			const res = await fetchPublicURL();
			savedUrl = res.url;
			draftUrl = res.url;
		} catch (err: unknown) {
			error = errMessage(err, 'Failed to load the address');
		} finally {
			loading = false;
		}
	}

	/**
	 * The save is an intent: the API answers 202 and the orchestrator applies
	 * it, re-provisioning SSO behind the scenes. Poll until the live address is
	 * the canonical one the save reported, so a change that was dropped or never
	 * landed gets reported instead of looking like it worked.
	 *
	 * The loop condition is the whole exit story: keep going while the address
	 * still differs and the apply window is open. Both outcomes fall out of the
	 * loop into the same if/else, so "it matched" and "it timed out" are read off
	 * the final value rather than tracked through breaks.
	 *
	 * The comparison is against the canonical origin the PUT returned rather
	 * than the raw typed string, because "bloud.example.com" is stored as
	 * "http://bloud.example.com" and the parser is not duplicated here.
	 */
	async function handleSaveAddress() {
		error = '';
		saving = true;
		try {
			const res = await setPublicURL(draftUrl.trim());
			const wanted = res.url;
			const deadline = Date.now() + APPLY_TIMEOUT;
			let live = await fetchPublicURL();
			while (live.url !== wanted && Date.now() < deadline) {
				await new Promise((r) => setTimeout(r, POLL_INTERVAL));
				live = await fetchPublicURL();
			}
			if (live.url === wanted) {
				savedUrl = live.url;
				draftUrl = live.url;
			} else {
				error = `Saved, but the address is still ${live.url}. Check the host-agent logs.`;
			}
		} catch (err: unknown) {
			error = errMessage(err, 'Failed to save the address');
		} finally {
			saving = false;
		}
	}

	function errMessage(err: unknown, fallback: string): string {
		if (err && typeof err === 'object' && 'message' in err) {
			return String((err as { message: unknown }).message);
		}
		return fallback;
	}
</script>

<section class="section address-section">
	<h2>Address</h2>
	<p class="section-description">
		Where you reach this Bloud from outside. The scheme and the port belong
		to the proxy: set them to whatever answers there, not to what Bloud
		listens on internally.
	</p>

	{#if loading}
		<div class="loading-state"><p>Loading address...</p></div>
	{:else}
		<!-- The consequence used to be the last sentence of the paragraph above,
		     in the same type and colour as the explanation, and a user could save
		     without ever meeting it. Saving this field restarts every app on the
		     instance, so it is now a callout that only appears when the value has
		     actually changed, states the blast radius, and has to be acknowledged
		     before Save works. The input is described by it, so the warning is read
		     with the field rather than hoping the user scrolled past it. -->
		{#if addressDirty}
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
						<input type="checkbox" bind:checked={acknowledged} />
						I understand my apps will briefly restart.
					</label>
				</div>
			</aside>
		{/if}

		<form class="address-form" onsubmit={(e) => { e.preventDefault(); handleSaveAddress(); }}>
			<input
				type="text"
				class="address-input"
				placeholder="https://bloud.example.com"
				aria-label="Public address"
				aria-describedby={addressDirty ? 'address-warning-title' : undefined}
				inputmode="url"
				autocomplete="off"
				autocapitalize="none"
				spellcheck="false"
				required
				disabled={saving}
				bind:value={draftUrl}
			/>
			<button
				class="btn btn-primary"
				type="submit"
				disabled={!addressDirty || saving || !draftUrl.trim() || saveBlocked}
			>
				{saving ? 'Applying…' : 'Save'}
			</button>
		</form>
	{/if}

	{#if error}
		<div class="error-message" role="alert">{error}</div>
	{/if}
</section>

<style>
	/* The section shell matches the other settings sections exactly: same
	   measure, same heading and description type. Each section carries its own
	   copy because the page's scoped styles do not reach into a child
	   component's markup. Address is the first section, so it has no separator
	   above it. */
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
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		opacity: 0.9;
	}

	.error-message {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: rgba(220, 38, 38, 0.05);
		border: 1px solid rgba(220, 38, 38, 0.15);
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
