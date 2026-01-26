<script lang="ts">
	interface Props {
		message?: string;
	}

	let { message = 'Something went wrong' }: Props = $props();
</script>

<div class="error-state">
	<p>{message}</p>
</div>

<style>
	.error-state {
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--color-error);
	}

	.error-state p {
		margin: 0;
	}
</style>
