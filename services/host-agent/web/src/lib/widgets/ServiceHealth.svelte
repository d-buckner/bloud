<script lang="ts">
	import { apps } from '$lib/stores/apps';
	import { AppStatus } from '$lib/types';
	import type { WidgetProps } from './registry';

	let { config }: WidgetProps = $props();

	const runningApps = $derived($apps.filter((a) => a.status === AppStatus.Running));
	const errorApps = $derived($apps.filter((a) => a.status === AppStatus.Error || a.status === AppStatus.Failed));
	const startingApps = $derived($apps.filter((a) => a.status === AppStatus.Installing || a.status === AppStatus.Starting));

	const allHealthy = $derived(errorApps.length === 0 && startingApps.length === 0);
</script>

<div class="service-health">
	{#if $apps.length === 0}
		<div class="empty">
			<span class="empty-text">No apps installed</span>
		</div>
	{:else}
		<div class="status-summary">
			<div class="status-item">
				<span class="status-dot running"></span>
				<span class="status-count">{runningApps.length}</span>
				<span class="status-label">Running</span>
			</div>
			{#if errorApps.length > 0}
				<div class="status-item">
					<span class="status-dot error"></span>
					<span class="status-count">{errorApps.length}</span>
					<span class="status-label">Error</span>
				</div>
			{/if}
			{#if startingApps.length > 0}
				<div class="status-item">
					<span class="status-dot starting"></span>
					<span class="status-count">{startingApps.length}</span>
					<span class="status-label">Starting</span>
				</div>
			{/if}
		</div>

		{#if allHealthy}
			<div class="health-message healthy">
				All services running normally
			</div>
		{:else if errorApps.length > 0}
			<div class="health-message warning">
				<span class="warning-icon">!</span>
				{errorApps.map((a) => a.display_name).join(', ')} {errorApps.length === 1 ? 'needs' : 'need'} attention
			</div>
		{/if}
	{/if}
</div>

<style>
	.service-health {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	.empty {
		text-align: center;
		padding: var(--space-md);
	}

	.empty-text {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.status-summary {
		display: flex;
		gap: var(--space-xl);
	}

	.status-item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.status-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}

	.status-dot.running {
		background: var(--color-success);
	}

	.status-dot.error {
		background: var(--color-error);
	}

	.status-dot.starting {
		background: var(--color-warning);
		animation: pulse 1.5s ease-in-out infinite;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.5; }
	}

	.status-count {
		font-weight: 600;
		font-size: 1.125rem;
	}

	.status-label {
		color: var(--color-text-secondary);
		font-size: 0.875rem;
	}

	.health-message {
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius-md);
		font-size: 0.875rem;
	}

	.health-message.healthy {
		background: rgba(22, 101, 52, 0.08);
		color: var(--color-success);
	}

	.health-message.warning {
		background: rgba(153, 27, 27, 0.08);
		color: var(--color-error);
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.warning-icon {
		width: 18px;
		height: 18px;
		background: var(--color-error);
		color: white;
		font-size: 12px;
		font-weight: 600;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
	}
</style>
