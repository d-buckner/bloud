<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	/**
	 * The frame around the installed apps, with the one word that says what the
	 * rectangle means and the one sentence that says what the reconciler is doing.
	 *
	 * The box is drawn by a node type of its own rather than by xyflow's built-in
	 * group node (which renders nothing at all) because an unlabeled rectangle is
	 * a claim without a subject: it sits behind most of the canvas, and nothing on
	 * the page said what being inside it meant.
	 *
	 * The note is the whole of the engine-level status, and it is empty most of
	 * the time. There is no status panel on this page: a floating card that reads
	 * "Idle" is a tax, and a dead reconciler looks exactly like a healthy one if
	 * the only evidence is a canvas of green dots. So the frame that encloses
	 * everything the reconciler owns is what changes when the reconciler stops
	 * being boring, and the border carries the same news a second time for
	 * someone scanning the canvas rather than reading it.
	 */
	interface NodeData {
		label?: string;
		note?: string;
		tone?: 'error' | 'info' | 'warn' | 'idle';
	}

	let { data }: { data: NodeData } = $props();

	const tone = $derived(data.tone ?? 'idle');
</script>

<div
	class="app-group"
	class:error={tone === 'error'}
	class:warn={tone === 'warn'}
	class:info={tone === 'info'}
	role="group"
	aria-label={data.note ? `${data.label ?? 'Installed apps'}: ${data.note}` : (data.label ?? 'Installed apps')}
>
	<span class="group-label">
		{data.label ?? 'Installed apps'}
		{#if data.note}
			<span class="note">{data.note}</span>
		{/if}
	</span>
</div>

<style>
	.app-group {
		box-sizing: border-box;
		position: relative;
		width: 100%;
		height: 100%;
		border: 1px solid var(--color-border-strong, #d6d3d1);
		border-radius: 12px;
		background: rgba(120, 113, 108, 0.03);
	}

	/* The frame takes the tone for the two states that are true of the whole set:
	   nothing is reconciling these apps, or the last attempt is old. A pass in
	   flight is not, and colouring the frame for it made the loudest line on the
	   page the one that said the least: nine tenths of the canvas outline turning
	   blue to report that one of a hundred nodes was mid-PreStart. The note says
	   it in words, in the tone colour, and that is enough. */
	.app-group.warn {
		border-color: var(--color-warning, #92400e);
	}

	.app-group.error {
		border-color: var(--color-error, #991b1b);
		border-style: dashed;
	}

	/* Set on the border the way a fieldset legend sits on its fieldset, so the
	   label is part of the frame instead of a caption floating near it. The
	   canvas color behind the text is what breaks the line behind the word. */
	.group-label {
		position: absolute;
		top: -9px;
		left: 14px;
		padding: 0 8px;
		background: var(--color-bg, #faf9f6);
		font-family: var(--font-serif, system-ui);
		font-size: 0.6875rem;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-secondary, #57534e);
	}

	/* The note is a sentence, not a second heading: no letterspacing, no caps,
	   and the tone color that the border already carries. */
	.note {
		margin-left: 8px;
		letter-spacing: 0;
		text-transform: none;
		font-weight: 500;
	}

	.info .note {
		color: var(--color-info, #0c4a6e);
	}

	.warn .note {
		color: var(--color-warning, #92400e);
	}

	.error .note {
		color: var(--color-error, #991b1b);
	}
</style>
