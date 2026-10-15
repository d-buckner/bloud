<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	interface Props {
		/** Any CSS width. */
		width?: string;
		/** Any CSS height. */
		height?: string;
		/** Any CSS border-radius. */
		radius?: string;
		class?: string;
	}

	// One block of a skeleton. It is a span rather than a div because a skeleton
	// is an inline-ish shape that a parent lays out, and `display: block` is set
	// here so it still fills a column.
	let {
		width = '100%',
		height = '1rem',
		radius = 'var(--radius-sm)',
		class: className = ''
	}: Props = $props();
</script>

<span class="skeleton {className}" style="width: {width}; height: {height}; border-radius: {radius};"></span>

<style>
	.skeleton {
		display: block;
		background: var(--color-skeleton);
		animation: skeleton-pulse 1.4s ease-in-out infinite;
	}

	/* The pulse is the only thing that says "this is arriving" rather than "this
	   is missing", so it is worth keeping. It is also the only moving thing on the
	   page during load, and for a reader who is sensitive to motion the page is
	   still a page without it. */
	@media (prefers-reduced-motion: reduce) {
		.skeleton {
			animation: none;
		}
	}
</style>
