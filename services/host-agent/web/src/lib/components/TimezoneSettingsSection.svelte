<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import Icon from './Icon.svelte';
	import { fetchTimezone, setTimezone } from '$lib/clients/settingsClient';
	import { pollUntil } from '$lib/store/effects/convergence';
	import { browserZone, offsetLabel, supportedZones, zoneOptions } from '$lib/utils/timezone';

	// Time zone state. One IANA name in, one name out, and "" is a value rather
	// than a missing value: it means Bloud states no zone.
	let savedZone = $state('');
	let draftZone = $state('');
	let loading = $state(true);
	let error = $state('');
	let saving = $state(false);
	// The acknowledgement for a changed zone. It gates Save for the same reason
	// the address section gates it: the change recreates every container.
	let acknowledged = $state(false);

	// The zone list comes from the browser, which is the only place that knows
	// which zones exist without shipping a copy of the zone database. See
	// `$lib/utils/timezone` for the fallback and the reason it may be short.
	const zones = supportedZones();
	const localZone = browserZone();

	// A saved zone the browser does not list still has to be selectable, or the
	// select would show the first option and a Save would change the zone the
	// operator did not touch.
	const options = $derived(zoneOptions(zones, [draftZone, savedZone, localZone]));

	const zoneDirty = $derived(draftZone !== savedZone);
	const saveBlocked = $derived(zoneDirty && !acknowledged);

	// Any edit withdraws the acknowledgement. An operator who agreed to restart
	// the apps on one zone has not agreed to the one they picked next.
	$effect(() => {
		draftZone;
		acknowledged = false;
	});

	// The offset line is a fact about the zone, not a clock, so it needs no timer
	// to stay true. See `offsetLabel` for why the form shows one at all.
	const offset = $derived(offsetLabel(draftZone));
	const suggestion = $derived(
		!savedZone && localZone && draftZone !== localZone ? localZone : ''
	);

	// The window is wider than the address setting's on purpose. Every container
	// on the instance is recreated here, in dependency order, and a stack with a
	// database-heavy app in it takes minutes rather than seconds.
	const POLL = { intervalMs: 500, timeoutMs: 60_000 };

	onMount(loadTimezone);

	async function loadTimezone() {
		loading = true;
		error = '';
		try {
			const res = await fetchTimezone();
			savedZone = res.zone;
			draftZone = res.zone;
		} catch (err: unknown) {
			error = errMessage(err, 'Failed to load the time zone');
		} finally {
			loading = false;
		}
	}

	/**
	 * The save is an intent: the API answers 202 and the orchestrator recreates
	 * the containers so they come back on the new clock. Read until the live zone
	 * is the one the save accepted, so a change that was dropped reads as a
	 * failure instead of looking like it worked.
	 *
	 * `pollUntil` owns the timer. A component that decides when data changes is
	 * what `scripts/no-component-timers.mjs` refuses, and the settings effects
	 * module already carries that shape for every other intent save here.
	 */
	async function handleSaveTimezone() {
		error = '';
		saving = true;
		try {
			const res = await setTimezone(draftZone);
			const live = await pollUntil(fetchTimezone, (s) => s.zone === res.zone, POLL);
			if (live.zone === res.zone) {
				savedZone = live.zone;
				draftZone = live.zone;
			} else {
				error =
					live.zone === ''
						? 'Saved, but no zone is in force yet. Check the host-agent logs.'
						: `Saved, but the zone is still ${live.zone}. Check the host-agent logs.`;
			}
		} catch (err: unknown) {
			error = errMessage(err, 'Failed to save the time zone');
		} finally {
			saving = false;
		}
	}

	function useSuggestion() {
		draftZone = suggestion;
	}

	function errMessage(err: unknown, fallback: string): string {
		if (err && typeof err === 'object' && 'message' in err) {
			return String((err as { message: unknown }).message);
		}
		return fallback;
	}
</script>

<section class="section timezone-section">
	<h2>Time zone</h2>
	<p class="section-description">
		The clock your apps run on. Bloud passes it to every container, so
		scheduled jobs, log lines, and timestamps agree with each other.
	</p>

	{#if loading}
		<div class="loading-state"><p>Loading time zone...</p></div>
	{:else}
		{#if suggestion}
			<p class="suggestion">
				Your browser is in <code>{suggestion}</code>.
				<button class="link" type="button" onclick={useSuggestion} disabled={saving}>
					Use it
				</button>
			</p>
		{/if}

		{#if zoneDirty}
			<aside class="callout" aria-labelledby="timezone-warning-title">
				<span class="callout-icon" aria-hidden="true">
					<Icon name="warning" size={16} />
				</span>
				<div class="callout-body">
					<strong id="timezone-warning-title">This restarts your apps</strong>
					<p>
						A container's environment is fixed when it is created, so a new zone
						means Bloud recreates every container it manages, in dependency order.
						Expect a few seconds per app, and longer for a stack with a database in
						it. Nothing is deleted and nobody is signed out.
					</p>
					<label class="acknowledge">
						<input type="checkbox" bind:checked={acknowledged} />
						I understand my apps will briefly restart.
					</label>
				</div>
			</aside>
		{/if}

		<form class="timezone-form" onsubmit={(e) => { e.preventDefault(); handleSaveTimezone(); }}>
			<label class="visually-hidden" for="timezone-select">Time zone</label>
			<select
				id="timezone-select"
				class="timezone-select"
				bind:value={draftZone}
				disabled={saving}
				aria-describedby={zoneDirty ? 'timezone-warning-title' : undefined}
			>
				<option value="">No zone (each app keeps its own default)</option>
				{#each options as zone (zone)}
					<option value={zone}>{zone}</option>
				{/each}
			</select>
			<button class="btn btn-primary" type="submit" disabled={!zoneDirty || saving || saveBlocked}>
				{saving ? 'Applying…' : 'Save'}
			</button>
		</form>

		{#if offset}
			<p class="preview">Right now that zone is <strong>{offset}</strong> from UTC.</p>
		{/if}
	{/if}

	{#if error}
		<div class="error-message" role="alert">{error}</div>
	{/if}
</section>

<style>
	/* The section shell (heading, measure) comes from `base.css`. The settings
	   grid owns the spacing between sections, so this section carries no rule of
	   its own any more. */
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

	.suggestion {
		margin: 0 0 var(--space-md) 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	.suggestion code {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
	}

	.link {
		border: 0;
		background: none;
		padding: 0;
		font: inherit;
		color: var(--color-accent);
		cursor: pointer;
		text-decoration: underline;
	}

	.link:disabled {
		color: var(--color-text-muted);
		cursor: not-allowed;
	}

	.timezone-form {
		display: flex;
		gap: var(--space-sm);
		max-width: 32rem;
	}

	.timezone-select {
		flex: 1;
		min-width: 0;
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.timezone-select:focus {
		border-color: var(--color-accent);
	}

	.timezone-select:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.preview {
		margin: var(--space-md) 0 0 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	/* The same callout the address section carries, for the same reason: the
	   consequence has to be met next to the control that causes it, not in a
	   sentence the operator can scroll past. */
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

	@media (max-width: 480px) {
		.timezone-form {
			flex-direction: column;
			align-items: stretch;
		}

		.timezone-form .btn {
			align-self: stretch;
		}
	}
</style>
