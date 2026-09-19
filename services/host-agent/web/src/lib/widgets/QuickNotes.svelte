<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { browser } from '$app/environment';

	const STORAGE_KEY = 'bloud-quick-notes';
	const DEBOUNCE_MS = 400;
	const SAVED_HOLD_MS = 1500;

	// The widget is mounted client-side only, but guard anyway so an SSR pass
	// that ever reaches this component cannot touch `localStorage`.
	let notes = $state(browser ? (localStorage.getItem(STORAGE_KEY) ?? '') : '');
	let saveState = $state<'idle' | 'saving' | 'saved'>('idle');

	let saveTimeout: ReturnType<typeof setTimeout> | undefined;
	let holdTimeout: ReturnType<typeof setTimeout> | undefined;

	$effect(() => () => {
		clearTimeout(saveTimeout);
		clearTimeout(holdTimeout);
	});

	function handleInput(event: Event) {
		notes = (event.target as HTMLTextAreaElement).value;
		saveState = 'saving';

		clearTimeout(saveTimeout);
		saveTimeout = setTimeout(() => {
			localStorage.setItem(STORAGE_KEY, notes);
			saveState = 'saved';
			clearTimeout(holdTimeout);
			holdTimeout = setTimeout(() => (saveState = 'idle'), SAVED_HOLD_MS);
		}, DEBOUNCE_MS);
	}
</script>

<div class="quick-notes">
	<textarea
		class="notes-input"
		placeholder="Jot something down…"
		aria-label="Notes"
		value={notes}
		oninput={handleInput}
	></textarea>
	<div class="notes-footer">
		<span class="save-status" aria-live="polite">
			{#if saveState === 'saving'}Saving…{:else if saveState === 'saved'}Saved{/if}
		</span>
	</div>
</div>

<style>
	.quick-notes {
		display: flex;
		flex-direction: column;
		height: 100%;
		gap: var(--space-xs);
	}

	.notes-input {
		flex: 1;
		min-height: 0;
		width: 100%;
		padding: 0;
		border: none;
		background: transparent;
		resize: none;
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		line-height: 1.5;
		color: var(--color-text);
	}

	.notes-input::placeholder {
		color: var(--color-text-muted);
	}

	.notes-input:focus {
		outline: none;
	}

	.notes-footer {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: var(--space-sm);
		font-family: var(--font-sans);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		flex-shrink: 0;
	}

	.save-status {
		min-height: 1em;
	}
</style>
