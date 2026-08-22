<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import Icon from './Icon.svelte';
	import { toasts } from '$lib/stores/toasts';

	const TONE_ICON: Record<string, string> = {
		success: 'check-circle',
		error: 'error-circle',
		info: 'info'
	};
</script>

{#if $toasts.length > 0}
	<div class="toast-container" role="status" aria-live="polite">
		{#each $toasts as toast (toast.id)}
			<div class="toast" class:success={toast.tone === 'success'} class:error={toast.tone === 'error'}>
				<Icon name={TONE_ICON[toast.tone] ?? 'info'} size={18} />
				<span class="toast-message">{toast.message}</span>
			</div>
		{/each}
	</div>
{/if}

<style>
	.toast-container {
		position: fixed;
		bottom: var(--space-xl);
		right: var(--space-xl);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		z-index: 100;
		max-width: 360px;
	}

	.toast {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
		font-size: 0.875rem;
		color: var(--color-text);
		animation: toast-in 0.2s ease;
	}

	.toast.success {
		border-color: var(--color-success);
		color: var(--color-success);
	}

	.toast.error {
		border-color: var(--color-error);
		color: var(--color-error);
	}

	.toast-message {
		color: var(--color-text);
	}

	@keyframes toast-in {
		from {
			opacity: 0;
			transform: translateY(8px);
		}
		to {
			opacity: 1;
			transform: translateY(0);
		}
	}
</style>
