<script lang="ts">
	interface Props {
		lines: string[];
		lineHeight?: number;
		overscan?: number;
		autoScroll?: boolean;
		onautoScrollChange?: (value: boolean) => void;
	}

	const DEFAULT_LINE_HEIGHT = 21;
	const DEFAULT_OVERSCAN = 5;
	const SCROLL_THRESHOLD = 50;

	let {
		lines,
		lineHeight = DEFAULT_LINE_HEIGHT,
		overscan = DEFAULT_OVERSCAN,
		autoScroll = true,
		onautoScrollChange
	}: Props = $props();

	let container: HTMLElement | undefined = $state();
	let scrollTop = $state(0);
	let clientHeight = $state(0);

	const totalHeight = $derived(lines.length * lineHeight);
	const startIndex = $derived(Math.max(0, Math.floor(scrollTop / lineHeight) - overscan));
	const endIndex = $derived(
		Math.min(lines.length, Math.ceil((scrollTop + clientHeight) / lineHeight) + overscan)
	);
	const visibleLines = $derived(
		lines.slice(startIndex, endIndex).map((line, i) => ({
			index: startIndex + i,
			text: line,
			top: (startIndex + i) * lineHeight
		}))
	);

	function handleScroll() {
		if (!container) return;
		scrollTop = container.scrollTop;
		clientHeight = container.clientHeight;

		const isAtBottom = container.scrollHeight - container.scrollTop - container.clientHeight < SCROLL_THRESHOLD;
		if (isAtBottom !== autoScroll) {
			onautoScrollChange?.(isAtBottom);
		}
	}

	$effect(() => {
		if (!autoScroll) return;
		if (!container) return;
		if (lines.length === 0) return;

		requestAnimationFrame(() => {
			if (container) {
				container.scrollTop = container.scrollHeight;
			}
		});
	});

	$effect(() => {
		if (!container) return;
		clientHeight = container.clientHeight;
	});
</script>

<div class="virtual-list" bind:this={container} onscroll={handleScroll}>
	<div class="virtual-list-inner" style:height="{totalHeight}px">
		{#if lines.length === 0}
			<div class="empty-logs">Waiting for logs...</div>
		{:else}
			{#each visibleLines as line (line.index)}
				<div class="log-line" style:top="{line.top}px" style:height="{lineHeight}px">
					{line.text}
				</div>
			{/each}
		{/if}
	</div>
</div>

<style>
	.virtual-list {
		height: 400px;
		overflow-y: auto;
		background: var(--color-bg);
		font-family: ui-monospace, 'SF Mono', Menlo, Monaco, 'Cascadia Code', monospace;
		font-size: 0.75rem;
		line-height: 1.5;
	}

	.virtual-list-inner {
		position: relative;
	}

	.log-line {
		position: absolute;
		left: 0;
		right: 0;
		padding: 1px var(--space-sm);
		white-space: pre-wrap;
		word-break: break-all;
		overflow: hidden;
	}

	.log-line:hover {
		background: var(--color-bg-subtle);
	}

	.empty-logs {
		color: var(--color-text-muted);
		text-align: center;
		padding: var(--space-xl);
		font-family: var(--font-serif);
		font-size: 0.875rem;
	}
</style>
