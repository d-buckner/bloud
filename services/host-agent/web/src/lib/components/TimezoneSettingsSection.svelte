<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Icon from './Icon.svelte';
	import { useTimezone } from '$lib/store';
	import TimeZonePicker from './TimeZonePicker.svelte';

	// The platform owns the clock, its browser zone list, the offset line and the
	// consent gate. This component owns the markup.
	const zone = useTimezone();
</script>

<section class="section timezone-section">
	<h2>Time zone</h2>
	<p class="section-description">
		The clock your apps run on. Bloud passes it to every container, so
		scheduled jobs, log lines, and timestamps agree with each other.
	</p>

	{#if zone.loading}
		<div class="loading-state"><p>Loading time zone...</p></div>
	{:else}
		{#if zone.suggestion}
			<p class="suggestion">
				Your browser is in <code>{zone.suggestion}</code>.
				<button class="link" type="button" onclick={() => zone.useSuggestion()}>
					Use it
				</button>
			</p>
		{/if}

		{#if zone.dirty}
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
						<input type="checkbox" bind:checked={zone.acknowledged} />
						I understand my apps will briefly restart.
					</label>
				</div>
			</aside>
		{/if}

		<form class="timezone-form" onsubmit={(e) => { e.preventDefault(); zone.save(); }}>
			<TimeZonePicker
				zones={zone.options}
				value={zone.edit}
				describedby={zone.dirty ? 'timezone-warning-title' : undefined}
				onchange={(picked) => (zone.edit = picked)}
			/>
			<button class="btn btn-primary" type="submit" disabled={!zone.dirty || zone.blocked}>
				Save
			</button>
		</form>

		{#if zone.offset}
			<p class="preview">Right now that zone is <strong>{zone.offset}</strong> from UTC.</p>
		{/if}
	{/if}

	{#if zone.error}
		<div class="error-message" role="alert">{zone.error}</div>
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
