<script lang="ts">
	interface Props {
		label?: string;
		type?: 'text' | 'password' | 'email';
		value?: string;
		placeholder?: string;
		disabled?: boolean;
		error?: string;
		onchange?: (value: string) => void;
	}

	let {
		label,
		type = 'text',
		value = $bindable(''),
		placeholder = '',
		disabled = false,
		error
	}: Props = $props();
</script>

<div class="field">
	{#if label}
		<label class="label">{label}</label>
	{/if}
	<input
		class="input"
		class:has-error={!!error}
		{type}
		{placeholder}
		{disabled}
		bind:value
	/>
	{#if error}
		<span class="error">{error}</span>
	{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.label {
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	.input {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
		font-size: 0.9375rem;
		font-family: var(--font-sans);
		transition: border-color 0.15s ease;
	}

	.input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.input.has-error {
		border-color: var(--color-error);
	}

	.input:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.error {
		font-size: 0.8125rem;
		color: var(--color-error);
	}
</style>
