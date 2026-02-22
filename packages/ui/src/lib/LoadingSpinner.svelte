<script lang="ts">
	interface Props {
		size?: 'sm' | 'md' | 'lg';
	}

	let { size = 'md' }: Props = $props();
</script>

<span class="spinner" class:sm={size === 'sm'} class:lg={size === 'lg'} aria-label="Loading"></span>

<style>
	.spinner {
		display: inline-block;
		width: 1.25rem;
		height: 1.25rem;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.7s linear infinite;
	}

	.spinner.sm {
		width: 0.875rem;
		height: 0.875rem;
	}

	.spinner.lg {
		width: 2rem;
		height: 2rem;
		border-width: 3px;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}
</style>
