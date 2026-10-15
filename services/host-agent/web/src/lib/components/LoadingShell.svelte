<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import LoadingGrid from './LoadingGrid.svelte';
	import Skeleton from './Skeleton.svelte';

	// The whole page, drawn in blocks, for the window between the document
	// arriving and the session read answering. It is the shape of the dashboard:
	// the rail, the page header, and the grid. Nothing here waits on data, because
	// the shell is known before the data is, and that is the whole reason a
	// skeleton can be drawn at all.
</script>

<div class="shell">
	<nav class="rail" aria-hidden="true">
		<div class="rail-logo">
			<Skeleton width="72px" height="24px" radius="var(--radius-md)" />
		</div>
		<div class="rail-nav">
			{#each Array(4) as _, i (i)}
				<div class="rail-row">
					<Skeleton width="20px" height="20px" radius="var(--radius-sm)" />
					<span class="rail-label">
						<Skeleton width="64px" height="12px" />
					</span>
				</div>
			{/each}
		</div>
		<div class="rail-foot">
			<Skeleton width="88px" height="12px" />
			<Skeleton width="56px" height="12px" />
		</div>
	</nav>

	<div class="content" aria-hidden="true">
		<div class="page">
			<header class="page-header">
				<div class="header-content">
					<Skeleton width="150px" height="28px" radius="var(--radius-md)" />
					<Skeleton width="220px" height="13px" />
				</div>
				<div class="header-actions">
					<Skeleton width="104px" height="38px" radius="var(--radius-md)" />
					<Skeleton width="150px" height="38px" radius="var(--radius-md)" />
				</div>
			</header>
			<LoadingGrid />
		</div>
	</div>

	<!-- The blocks are decoration, so they are hidden from a screen reader. This
	     is the part that should be read: a skeleton that announces nothing leaves
	     someone using a screen reader with no signal at all that anything is
	     happening. -->
	<p class="visually-hidden" role="status">Loading Bloud</p>
</div>

<style>
	.shell {
		display: flex;
		min-height: 100vh;
	}

	/* Mirrors Sidebar.svelte: fixed rail, main content offset by its width, and
	   the same 64px icon rail below 768px. */
	.rail {
		width: var(--sidebar-width);
		background: var(--color-bg-elevated);
		border-right: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		position: fixed;
		top: 0;
		left: 0;
		bottom: 0;
	}

	.rail-logo {
		padding: var(--space-xl) var(--space-lg);
		border-bottom: 1px solid var(--color-border-subtle);
	}

	.rail-nav {
		padding: var(--space-lg) var(--space-md);
		flex: 1;
	}

	.rail-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		margin-bottom: var(--space-xs);
	}

	/* Wrapped rather than styled through the child, so this stylesheet owns the
	   element it hides on the icon-rail breakpoint. */
	.rail-label {
		display: block;
	}

	.rail-foot {
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border-subtle);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.content {
		flex: 1;
		margin-left: var(--sidebar-width);
		min-height: 100vh;
	}

	/* Mirrors the page shell every route uses, so the header skeleton lands where
	   the real header will. */
	.page {
		width: 100%;
		max-width: 1300px;
		margin: 0 auto;
		padding: var(--space-2xl) var(--space-xl);
	}

	.page-header {
		display: flex;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-lg);
		margin-bottom: var(--space-xl);
		padding-bottom: var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.header-content {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	@media (max-width: 768px) {
		.rail {
			width: 64px;
		}

		.rail-logo {
			padding: var(--space-lg) var(--space-sm);
		}

		.rail-nav {
			padding: var(--space-lg) var(--space-sm);
		}

		.rail-row {
			justify-content: center;
			padding: var(--space-sm);
		}

		.rail-label {
			display: none;
		}

		.rail-foot {
			padding: var(--space-md) var(--space-sm);
			align-items: center;
		}

		.content {
			margin-left: 64px;
		}

		.page {
			padding: var(--space-xl) var(--space-md);
		}

		.page-header {
			flex-direction: column;
			align-items: flex-start;
		}
	}
</style>
