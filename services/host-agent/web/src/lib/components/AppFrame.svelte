<script lang="ts">
	/**
	 * AppFrame - Single iframe for an embedded app
	 *
	 * Handles:
	 * - Bootstrap config execution (once per app)
	 * - Iframe loading state
	 * - URL sync between iframe and browser
	 *
	 * This component is kept alive when switching tabs - only visibility changes.
	 */

	import { onMount } from "svelte";
	import type { App } from "$lib/types";
	import { bootstrapApp } from "$lib/services/appBootstrap";
	import { getStatusConfig, isAppReady } from "$lib/utils/statusConfig";
	import { tabs } from "$lib/stores/tabs";
	import { getEmbedUrl, extractRelativePath } from "$lib/utils/embedUrl";
	import Icon from "./Icon.svelte";

	interface Props {
		app: App;
		path: string;
		isActive: boolean;
		onNavigate?: (path: string) => void;
	}

	let { app, path, isActive, onNavigate }: Props = $props();

	let iframeEl = $state<HTMLIFrameElement | null>(null);
	let bootstrapped = $state(false);
	let loading = $state(true);
	let iframeLoading = $state(true);
	let error = $state<string | null>(null);

	// Track the last path we set on the iframe to avoid loops
	let lastSetPath = "";

	// Run bootstrap when component mounts
	onMount(async () => {
		const result = await bootstrapApp(app.name);
		if (result.success) {
			bootstrapped = true;
		} else {
			error = result.error ?? "Bootstrap failed";
		}
		loading = false;
	});

	function handleIframeLoad() {
		iframeLoading = false;
		syncUrlFromIframe();
	}

	function syncUrlFromIframe() {
		const contentWindow = iframeEl?.contentWindow;
		if (!contentWindow || !isActive) return;

		try {
			const {pathname, search} = contentWindow.location;
			const relativePath = extractRelativePath(pathname, app.name);
			if (relativePath === null) {
				return;
			}

			const fullPath = relativePath + search;

			// Save path to store
			tabs.setPath(app.name, fullPath);

			// Notify parent for URL update
			if (onNavigate && fullPath !== lastSetPath) {
				onNavigate(fullPath);
			}
		} catch {
			// Cross-origin error - ignore
		}
	}

	// Update iframe src when path changes externally (user navigation)
	$effect(() => {
		if (path !== lastSetPath && iframeEl && bootstrapped) {
			lastSetPath = path;
			const newSrc = getEmbedUrl(app.name, path);
			if (iframeEl.src !== newSrc) {
				iframeEl.src = newSrc;
			}
		}
	});

	// Derive state for status display
	const frameState = $derived({ loading, error, bootstrapped });
	const isReady = $derived(isAppReady(app, frameState));
	const statusConfig = $derived(getStatusConfig(app, frameState));
</script>

<div class="app-frame" class:active={isActive} class:hidden={!isActive}>
	{#if statusConfig}
		<div class="status-container">
			{#if statusConfig.spinner}
				<div
					class="spinner"
					class:large={statusConfig.spinner === "large"}
				></div>
			{/if}
			{#if statusConfig.icon}
				<div class="status-icon {statusConfig.icon.class}">
					<Icon name={statusConfig.icon.name} size={32} />
				</div>
			{/if}
			<p class={statusConfig.titleClass}>{statusConfig.title}</p>
			{#if statusConfig.subtitle}
				<span class="status-text">{statusConfig.subtitle}</span>
			{/if}
		</div>
	{/if}

	{#if isReady}
		<div class="iframe-wrapper">
			{#if iframeLoading}
				<div class="iframe-loading">
					<div class="spinner"></div>
					<p>Connecting to {app.display_name}...</p>
				</div>
			{/if}
			<iframe
				bind:this={iframeEl}
				class="app-iframe"
				src={getEmbedUrl(app.name, path)}
				title={app.display_name}
				onload={handleIframeLoad}
			></iframe>
		</div>
	{/if}
</div>

<style>
	.app-frame {
		position: absolute;
		inset: 0;
	}

	.app-frame.hidden {
		display: none;
	}

	.iframe-wrapper {
		position: relative;
		width: 100%;
		height: 100%;
	}

	.app-iframe {
		width: 100%;
		height: 100%;
		border: none;
	}

	.iframe-loading {
		position: absolute;
		top: 0;
		left: 0;
		right: 0;
		bottom: 0;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-md);
		background: var(--color-bg);
		z-index: 1;
	}

	.iframe-loading p {
		margin: 0;
		color: var(--color-text-muted);
	}

	.status-container {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		height: 100%;
		gap: var(--space-sm);
		color: var(--color-text-muted);
	}

	.status-container p {
		margin: 0;
		font-size: 1.125rem;
		color: var(--color-text);
	}

	.status-text {
		font-size: 0.875rem;
	}

	.spinner {
		width: 32px;
		height: 32px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	.spinner.large {
		width: 40px;
		height: 40px;
		border-width: 3px;
		margin-bottom: var(--space-sm);
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.status-icon {
		margin-bottom: var(--space-sm);
	}

	.status-icon.stopped {
		color: #f59e0b;
	}

	.status-icon.error {
		color: var(--color-error);
	}

	.error-text {
		color: var(--color-error);
	}
</style>
