<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { filterZones } from '$lib/utils/timezone';

	/**
	 * A searchable single-select for IANA time zones.
	 *
	 * A native `<select>` is the wrong control here: the browser offers about 400
	 * zones, and finding `America/New_York` in an alphabetical list of 400 is a
	 * scroll, not a choice. The field filters as the operator types, and the
	 * keyboard keeps working the way a combobox is expected to.
	 *
	 * The empty value is a real choice, not a missing one: it means Bloud states
	 * no zone. It leads the list while the field is empty, so it stays reachable.
	 */
	interface Props {
		value: string;
		zones: string[];
		disabled?: boolean;
		/** Id of an element that describes the field, such as the restart warning. */
		describedby?: string;
		/** The accessible label. The field carries the zone name, not a hint. */
		label?: string;
		onchange: (zone: string) => void;
	}

	let {
		value,
		zones,
		disabled = false,
		describedby,
		label = 'Time zone',
		onchange
	}: Props = $props();

	const NO_ZONE = 'No zone (each app keeps its own default)';
	/** Enough rows to scan, few enough to render on every keystroke. */
	const LIMIT = 50;

	const id = $props.id();
	const listboxId = `${id}-listbox`;
	const optionId = (index: number) => `${id}-option-${index}`;

	let typed = $state<string | null>(null);
	// The text the field shows. `null` means "the chosen zone": typing sets it, a
	// commit or a blur clears it, and a value set elsewhere (the "your browser is
	// in" suggestion) shows through without a mirror effect.
	let query = $derived(typed ?? value);
	let open = $state(false);
	let active = $state(0);
	let input = $state<HTMLInputElement | null>(null);

	let options = $derived(
		query.trim() === ''
			? [
					{ value: '', label: NO_ZONE },
					...zones.slice(0, LIMIT - 1).map((zone) => ({ value: zone, label: zone }))
				]
			: filterZones(query, zones, LIMIT).map((zone) => ({ value: zone, label: zone }))
	);

	function openList() {
		open = true;
		const selected = options.findIndex((option) => option.value === value);
		active = selected === -1 ? 0 : selected;
	}

	function commit(option: { value: string; label: string } | undefined) {
		if (!option) return;
		onchange(option.value);
		typed = null;
		open = false;
		input?.focus();
	}

	function onInput(event: Event) {
		typed = (event.currentTarget as HTMLInputElement).value;
		open = true;
		active = 0;
	}

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'ArrowDown') {
			event.preventDefault();
			if (!open) openList();
			else active = Math.min(active + 1, options.length - 1);
		} else if (event.key === 'ArrowUp') {
			event.preventDefault();
			active = Math.max(active - 1, 0);
		} else if (event.key === 'Enter') {
			// While the list is open Enter chooses. While it is closed it belongs
			// to the form, which is what saves the section.
			if (open) {
				event.preventDefault();
				commit(options[active]);
			}
		} else if (event.key === 'Escape') {
			if (open) {
				event.preventDefault();
				open = false;
				typed = null;
			}
		}
	}

	/**
	 * Close when the focus leaves the field and its list, and drop the text the
	 * operator did not commit. A half-typed query is not a zone, and leaving it in
	 * the field makes the form disagree with the value it will save.
	 */
	function onFocusOut(event: FocusEvent & { currentTarget: HTMLDivElement }) {
		const next = event.relatedTarget as Node | null;
		if (next && event.currentTarget.contains(next)) return;
		open = false;
		typed = null;
	}

	/**
	 * The rows are buttons, not `<li>`s with click handlers.
	 *
	 * The keyboard belongs to the combobox input through `aria-activedescendant`,
	 * which is the ARIA pattern, but the pointer needs a real control: a clickable
	 * `<li>` is a non-interactive element standing in for a button, and every
	 * linter says so. `role="option"` on the button keeps the listbox semantics
	 * while the button keeps the pointer and the tap target honest. `tabindex="-1"`
	 * keeps them out of the Tab order, where the input already is.
	 */
</script>

<div class="timezone-picker" onfocusout={onFocusOut}>
	<label class="visually-hidden" for={`${id}-input`}>{label}</label>
	<input
		bind:this={input}
		id={`${id}-input`}
		class="timezone-input"
		type="text"
		role="combobox"
		aria-expanded={open}
		aria-controls={listboxId}
		aria-autocomplete="list"
		aria-activedescendant={open && options[active] ? optionId(active) : undefined}
		aria-describedby={describedby}
		placeholder={NO_ZONE}
		autocomplete="off"
		spellcheck="false"
		{disabled}
		value={query}
		onfocus={openList}
		oninput={onInput}
		onkeydown={onKeydown}
	/>

	{#if open && options.length > 0}
		<div class="timezone-list" id={listboxId} role="listbox" aria-label={label}>
			{#each options as option, index (option.value)}
				<button
					type="button"
					role="option"
					tabindex="-1"
					id={optionId(index)}
					class="timezone-option"
					class:active={index === active}
					aria-selected={option.value === value}
					onmousedown={(event) => event.preventDefault()}
					onclick={() => commit(option)}
					onmouseenter={() => (active = index)}
				>
					{option.label}
				</button>
			{/each}
		</div>
	{/if}
</div>

<style>
	.timezone-picker {
		position: relative;
		flex: 1;
		min-width: 0;
	}

	.timezone-input {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.timezone-input:focus {
		border-color: var(--color-accent);
	}

	.timezone-input:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.timezone-list {
		position: absolute;
		z-index: 20;
		top: calc(100% + 2px);
		left: 0;
		right: 0;
		padding: var(--space-xs);
		max-height: 15rem;
		overflow-y: auto;
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-popover);
	}

	.timezone-option {
		display: flex;
		align-items: center;
		width: 100%;
		min-height: var(--tap-target-min);
		padding: 0 var(--space-sm);
		background: none;
		border: none;
		border-radius: var(--radius-sm);
		text-align: left;
		font-family: var(--font-serif);
		font-size: 0.875rem;
		color: var(--color-text);
		cursor: pointer;
	}

	.timezone-option.active {
		background: var(--color-bg-subtle);
	}

	/* The chosen zone is marked in place as well as by the field's text, so the
	   list says which row is already in force while it is open. */
	.timezone-option[aria-selected='true'] {
		font-weight: 600;
	}
</style>
