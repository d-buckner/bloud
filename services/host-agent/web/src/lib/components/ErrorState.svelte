<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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
