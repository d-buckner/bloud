<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import Modal from './Modal.svelte';
	import AppIcon from './AppIcon.svelte';
	import CloseButton from './CloseButton.svelte';
	import Icon from './Icon.svelte';
	import { appProgress, recentActivity } from '$lib/stores/appProgress';
	import { deriveTimeline } from '$lib/services/installTimeline';
	import type { App } from '$lib/types';

	interface Props {
		app: App | null;
		onclose: () => void;
		onretry?: (appName: string) => void;
	}

	let { app, onclose, onretry }: Props = $props();

	let progress = $derived(app ? $appProgress[app.catalog_id] ?? null : null);
	let steps = $derived(app ? deriveTimeline(app.status, progress) : []);
	let isFailed = $derived(app?.status === 'failed' || app?.status === 'error');
	let retrying = $state(false);

	function formatTime(at?: number): string {
		if (!at) return '';
		return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
	}

	function openLogs() {
		if (!app) return;
		// The logs endpoint is an SSE text stream; a plain tab renders it.
		window.open(`/api/apps/${app.catalog_id}/logs`, '_blank');
	}

	async function doRetry() {
		if (!app || retrying) return;
		retrying = true;
		try {
			await onretry?.(app.catalog_id);
		} finally {
			retrying = false;
		}
	}

	function stepIcon(state: string): string {
		if (state === 'done') return 'check-circle';
		if (state === 'failed') return 'error-circle';
		return '';
	}
</script>

<Modal open={app !== null} onclose={onclose} size="lg">
	{#if app}
		<header class="modal-header">
			<div class="modal-app-header">
				<AppIcon appName={app.catalog_id} displayName={app.display_name} size="lg" />
				<div class="modal-app-info">
					<h2>{app.display_name}</h2>
					<span class="modal-app-status" class:failed={isFailed}>
						{app.status}
					</span>
				</div>
			</div>
			<CloseButton onclick={onclose} />
		</header>

		<div class="modal-body">
			{#if isFailed && (app.last_error || progress?.error)}
				<div class="error-box">
					<Icon name="error-circle" size={18} />
					<p class="error-text">{app.last_error || progress?.error}</p>
				</div>
			{/if}

			<h3 class="section-heading">Install progress</h3>
			<ol class="timeline">
				{#each steps as step (step.id)}
					<li class="step" class:done={step.state === 'done'} class:current={step.state === 'current'} class:failed={step.state === 'failed'}>
						<span class="step-marker">
							{#if stepIcon(step.state)}
								<Icon name={stepIcon(step.state)} size={16} />
							{:else if step.state === 'current'}
								<span class="step-spinner"></span>
							{:else}
								<span class="step-dot"></span>
							{/if}
						</span>
						<span class="step-label">{step.label}</span>
						{#if step.detail}
							<span class="step-detail">{step.detail}</span>
						{/if}
						{#if step.at}
							<span class="step-time">{formatTime(step.at)}</span>
						{/if}
					</li>
				{/each}
			</ol>

			{#if $recentActivity.length > 0}
				<h3 class="section-heading">Recent activity</h3>
				<ul class="activity-list">
					{#each $recentActivity.slice(-6).reverse() as entry (entry.time + entry.event)}
						<li>
							<span class="activity-time">{formatTime(new Date(entry.time).getTime())}</span>
							<span class="activity-event">{entry.event}</span>
							{#if entry.detail}
								<span class="activity-detail">{entry.detail}</span>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</div>

		<footer class="modal-footer">
			<button class="btn btn-secondary" onclick={openLogs}>
				<Icon name="external-link" size={16} />
				View logs
			</button>
			{#if isFailed}
				<button class="btn btn-primary" onclick={doRetry} disabled={retrying}>
					<Icon name="refresh" size={16} />
					{#if retrying}Retrying...{:else}Retry install{/if}
				</button>
			{/if}
		</footer>
	{/if}
</Modal>

<style>
	.modal-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.modal-app-header {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		flex: 1;
	}

	.modal-app-info h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 500;
	}

	.modal-app-status {
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		color: var(--color-text-muted);
	}

	.modal-app-status.failed {
		color: var(--color-error);
	}

	.modal-body {
		padding: var(--space-lg);
		max-height: 60vh;
		overflow-y: auto;
	}

	.modal-footer {
		display: flex;
		gap: var(--space-sm);
		justify-content: flex-end;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.error-box {
		display: flex;
		align-items: flex-start;
		gap: var(--space-sm);
		padding: var(--space-md);
		background: var(--color-error-bg);
		color: var(--color-error);
		border-radius: var(--radius-md);
		margin-bottom: var(--space-lg);
	}

	.error-text {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text);
		white-space: pre-wrap;
		word-break: break-word;
	}

	.section-heading {
		margin: 0 0 var(--space-md) 0;
		font-size: 0.8125rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}

	.timeline {
		list-style: none;
		margin: 0 0 var(--space-lg) 0;
		padding: 0;
	}

	.step {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-xs) 0;
		font-size: 0.9375rem;
		color: var(--color-text-muted);
	}

	.step.done {
		color: var(--color-text-secondary);
	}

	.step.current {
		color: var(--color-text);
		font-weight: 500;
	}

	.step.failed {
		color: var(--color-error);
		font-weight: 500;
	}

	.step-marker {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 20px;
	}

	.step.done .step-marker {
		color: var(--color-success);
	}

	.step.failed .step-marker {
		color: var(--color-error);
	}

	.step-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-border);
	}

	.step-spinner {
		width: 14px;
		height: 14px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.step-detail {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.step-time {
		margin-left: auto;
		font-size: 0.75rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
	}

	.activity-list {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	.activity-list li {
		display: flex;
		align-items: baseline;
		gap: var(--space-sm);
		padding: 3px 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.activity-time {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.activity-event {
		font-weight: 500;
	}

	.activity-detail {
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.btn {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn-primary {
		background: var(--color-accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		background: var(--color-accent-hover);
	}

	.btn-primary:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn-secondary {
		background: var(--color-bg-elevated);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover {
		background: var(--color-bg-subtle);
	}
</style>
