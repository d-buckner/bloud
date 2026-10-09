<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import type { Snippet } from 'svelte';

	interface Props {
		open: boolean;
		onclose: () => void;
		/**
		 * id of the element inside the modal that names it, normally its
		 * heading. Required: a dialog whose name comes from its whole body is
		 * what a screen reader has to listen to on every open.
		 */
		labelledBy: string;
		/** id of the element inside the modal that explains the consequence. */
		describedBy?: string;
		/** `alertdialog` for a confirmation that interrupts with a consequence. */
		dialogRole?: 'dialog' | 'alertdialog';
		size?: 'default' | 'lg' | 'xl' | 'fullscreen';
		children: Snippet;
	}

	let {
		open,
		onclose,
		labelledBy,
		describedBy = undefined,
		dialogRole = 'dialog',
		size = 'default',
		children
	}: Props = $props();

	let dialog: HTMLDialogElement | undefined = $state();

	// Plain instance state on purpose. These are never read by the template, and
	// making them $state would make the open effect depend on its own writes.
	/** Where the dialog lived before it was portal'd to `body`. */
	let portalHome: HTMLElement | null = null;
	let portalAnchor: Node | null = null;
	let inerted: HTMLElement[] = [];

	/**
	 * Everything a modal has to take over to be a real dialog.
	 *
	 * `<dialog>` + `showModal()` gives the top layer, the backdrop, and a
	 * browser-enforced sequential focus scope, but it does not lock the page
	 * scroll and it does not put `inert` on the rest of the document, so
	 * assistive tech can still walk out behind the modal. Both are added here.
	 *
	 * The dialog is moved to `document.body` while open because a dialog nested
	 * inside the page content cannot make its own ancestors inert: the sidebar
	 * and the rest of the shell are siblings of an *ancestor* of the dialog, so
	 * no subset of the dialog's own ancestors can exclude them. Once the dialog
	 * is a direct child of `body`, every other element child of `body` is the
	 * background and can be marked inert exactly.
	 */
	$effect(() => {
		if (!dialog || !open) return;

		const el = dialog;
		const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;

		portalIn(el);
		inertBackground(el);
		lockPageScroll();

		if (!el.open) el.showModal();
		focusFirstControl(el);

		return () => {
			if (el.open) el.close();
			releaseBackground();
			portalOut(el);
			if (trigger && trigger.isConnected) trigger.focus();
		};
	});

	function portalIn(el: HTMLElement) {
		if (el.parentElement === document.body) return;
		portalHome = el.parentElement;
		portalAnchor = el.nextSibling;
		document.body.appendChild(el);
	}

	function portalOut(el: HTMLElement) {
		if (!portalHome || el.parentElement === portalHome) return;
		// The anchor can be gone if the caller re-rendered around the dialog while
		// it was portal'd out; appending is the only remaining option then.
		if (portalAnchor && portalHome.contains(portalAnchor)) {
			portalHome.insertBefore(el, portalAnchor);
		} else {
			portalHome.appendChild(el);
		}
		portalHome = null;
		portalAnchor = null;
	}

	function inertBackground(el: HTMLElement) {
		for (const node of Array.from(document.body.children)) {
			if (node === el || !(node instanceof HTMLElement) || node.tagName === 'SCRIPT') continue;
			// Recorded even when something already set `inert`, because the modal
			// that set it may close first and would otherwise leave the background
			// inert with no dialog left to release it.
			node.inert = true;
			inerted.push(node);
		}
	}

	function lockPageScroll() {
		document.body.style.overflow = 'hidden';
	}

	/**
	 * Lift the scroll lock and the inert background only when no other modal is
	 * still open. A confirmation opened on top of a modal owns the same two
	 * things, and the modal underneath releasing them would leave the topmost
	 * dialog scrolling and its background reachable.
	 */
	function releaseBackground() {
		if (document.querySelector('dialog[open]')) return;
		for (const node of inerted) node.inert = false;
		inerted = [];
		document.body.style.overflow = '';
	}

	function focusFirstControl(el: HTMLElement) {
		const target = el.querySelector<HTMLElement>('[data-autofocus]') ?? focusableWithin(el)[0] ?? el;
		target.focus();
	}

	const FOCUSABLE = [
		'a[href]',
		'button:not([disabled])',
		'input:not([disabled]):not([type="hidden"])',
		'select:not([disabled])',
		'textarea:not([disabled])',
		'[tabindex]:not([tabindex="-1"])',
		'[contenteditable]:not([contenteditable="false"])'
	].join(',');

	function focusableWithin(el: HTMLElement): HTMLElement[] {
		return Array.from(el.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
			(node) => node.offsetParent !== null || node === document.activeElement
		);
	}

	/**
	 * Keep Tab inside the dialog. Browsers already scope sequential focus
	 * navigation to a modal `<dialog>`; this is the same guarantee written down
	 * rather than assumed, and it also catches focus that arrived on the dialog
	 * element itself.
	 */
	function trapTab(e: KeyboardEvent) {
		if (e.key !== 'Tab' || !dialog) return;
		const items = focusableWithin(dialog);
		if (items.length === 0) {
			e.preventDefault();
			dialog.focus();
			return;
		}
		// Focus can be on the dialog itself (nothing focusable inside, or a click on
		// its padding). Pull it into the dialog before the browser picks a target,
		// which would be the first thing in document order outside it.
		if (!dialog.contains(document.activeElement)) {
			e.preventDefault();
			(e.shiftKey ? items.at(-1) : items[0])?.focus();
			return;
		}
		wrapAtEdges(e, items);
	}

	/**
	 * Only the two ends of the list are ever reached by Tab. Everything between
	 * them the browser already keeps inside the dialog.
	 */
	function wrapAtEdges(e: KeyboardEvent, items: HTMLElement[]) {
		const active = document.activeElement;
		const atStart = active === items[0] || active === dialog;
		if (e.shiftKey && atStart) {
			e.preventDefault();
			items.at(-1)?.focus();
			return;
		}
		if (!e.shiftKey && active === items.at(-1)) {
			e.preventDefault();
			items[0].focus();
		}
	}

	function handleClick(e: MouseEvent) {
		if (e.target === dialog) {
			onclose();
		}
	}

	function handleCancel(e: Event) {
		e.preventDefault();
		onclose();
	}
</script>

<dialog
	bind:this={dialog}
	class="modal"
	class:modal-lg={size === 'lg'}
	class:modal-xl={size === 'xl'}
	class:modal-fullscreen={size === 'fullscreen'}
	role={dialogRole}
	aria-modal="true"
	aria-labelledby={labelledBy}
	aria-describedby={describedBy}
	tabindex="-1"
	onclick={handleClick}
	onkeydown={trapTab}
	oncancel={handleCancel}
>
	<div
		class="modal-content"
		onclick={(e) => e.stopPropagation()}
		onkeydown={(e) => {
			trapTab(e);
			e.stopPropagation();
		}}
		role="presentation"
	>
		{#if open}
			{@render children()}
		{/if}
	</div>
</dialog>

<style>
	dialog.modal {
		padding: 0;
		border: none;
		border-radius: var(--radius-lg);
		width: 90%;
		max-width: 480px;
		max-height: 85vh;
		overflow: visible;
		background: transparent;
		box-shadow: var(--shadow-md), 0 20px 40px rgba(0, 0, 0, 0.15);
	}

	dialog.modal:focus {
		outline: none;
	}

	dialog.modal::backdrop {
		background: rgba(0, 0, 0, 0.4);
		backdrop-filter: blur(2px);
	}

	dialog.modal-lg {
		max-width: 540px;
	}

	dialog.modal-xl {
		max-width: 900px;
		max-height: 90vh;
	}

	dialog.modal-xl .modal-content {
		max-height: 90vh;
	}

	dialog.modal-fullscreen {
		width: 100%;
		max-width: 100%;
		height: 100%;
		max-height: 100%;
		border-radius: 0;
	}

	dialog.modal-fullscreen .modal-content {
		max-height: 100%;
		height: 100%;
		border-radius: 0;
	}

	.modal-content {
		background: var(--color-bg-elevated);
		border-radius: var(--radius-lg);
		max-height: 85vh;
		overflow-y: auto;
	}
</style>
