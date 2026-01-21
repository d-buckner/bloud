<script lang="ts">
	import { page } from '$app/state';
	import Icon from './Icon.svelte';
	import AppIcon from './AppIcon.svelte';
	import { openAppsList } from '$lib/stores/tabs';
	import { openApp, closeApp } from '$lib/services/navigation';

	interface User {
		id: number;
		username: string;
	}

	interface Props {
		collapsed?: boolean;
		user?: User | null;
	}

	let { collapsed = $bindable(false), user = null }: Props = $props();

	let currentPath = $derived(page.url.pathname);
	let isAppView = $derived(currentPath.startsWith('/apps/'));
	let manualExpand = $state(false);

	// Update the bound collapsed state
	$effect(() => {
		collapsed = isAppView && !manualExpand;
	});

	// Reset manual expand when leaving app view
	$effect(() => {
		if (!isAppView) {
			manualExpand = false;
		}
	});

	const navItems = [
		{ href: '/', label: 'Apps', icon: 'home' },
		{ href: '/catalog', label: 'Catalog', icon: 'store' },
		{ href: '/versions', label: 'History', icon: 'history' }
	];

	function handleTabClick(e: MouseEvent, appName: string) {
		e.preventDefault();
		openApp(appName);
	}

	function handleCloseTab(e: MouseEvent, appName: string) {
		e.stopPropagation();
		e.preventDefault();
		closeApp(appName, currentPath);
	}
</script>

<nav class="sidebar" class:collapsed>
	<div class="logo">
		{#if collapsed}
			<button class="expand-btn" onclick={() => manualExpand = true} title="Expand sidebar">
				<Icon name="menu" size={20} />
			</button>
		{:else}
			<span class="logo-text">Bloud</span>
			{#if isAppView}
				<button class="collapse-btn" onclick={() => manualExpand = false} title="Collapse sidebar">
					<Icon name="chevron-left" size={16} />
				</button>
			{/if}
		{/if}
	</div>

	<ul class="nav-links">
		{#each navItems as item}
			<li>
				<a href={item.href} class:active={currentPath === item.href}>
					<span class="nav-icon">
						<Icon name={item.icon} size={20} />
					</span>
					<span>{item.label}</span>
				</a>
			</li>
		{/each}
	</ul>

	{#if $openAppsList.length > 0}
		<div class="open-apps">
			<div class="open-apps-label">Open</div>
			<ul class="app-tabs">
				{#each $openAppsList as app}
					<li class="app-tab-item">
						<a
							href={`/apps/${app.name}`}
							class="app-tab"
							class:active={currentPath.startsWith(`/apps/${app.name}`)}
							onclick={(e) => handleTabClick(e, app.name)}
						>
							<span class="app-tab-icon">
								<AppIcon appName={app.name} displayName={app.display_name} size="sm" />
							</span>
							<span class="app-tab-name">{app.display_name}</span>
						</a>
						<button
							class="close-tab"
							onclick={(e) => handleCloseTab(e, app.name)}
							title="Close"
						>
							<Icon name="close" size={14} />
						</button>
					</li>
				{/each}
			</ul>
		</div>
	{/if}

	<div class="sidebar-footer">
		{#if user}
			<div class="user-section">
				<span class="username" title={user.username}>
					<Icon name="user" size={16} />
					<span class="username-text">{user.username}</span>
				</span>
				<form action="/auth/logout" method="POST" class="logout-form">
					<button type="submit" class="logout-btn" title="Sign out">
						<Icon name="logout" size={16} />
					</button>
				</form>
			</div>
		{/if}
		<a href="/versions" class="version">v0.1.0</a>
	</div>
</nav>

<style>
	.sidebar {
		width: var(--sidebar-width);
		background: var(--color-bg-elevated);
		border-right: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		position: fixed;
		top: 0;
		left: 0;
		bottom: 0;
		z-index: 50;
		transition: width 0.2s ease;
	}

	.logo {
		padding: var(--space-xl) var(--space-lg);
		border-bottom: 1px solid var(--color-border-subtle);
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.logo-text {
		font-size: 1.375rem;
		font-weight: 500;
		letter-spacing: -0.02em;
	}

	.expand-btn,
	.collapse-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: var(--space-xs);
		border-radius: var(--radius-sm);
		transition: all 0.15s ease;
	}

	.expand-btn:hover,
	.collapse-btn:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.expand-btn {
		margin: 0 auto;
	}

	.nav-links {
		list-style: none;
		margin: 0;
		padding: var(--space-lg) var(--space-md);
		flex: 1;
	}

	.nav-links li {
		margin-bottom: var(--space-xs);
	}

	.nav-links a {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius-md);
		text-decoration: none;
		color: var(--color-text-secondary);
		transition: all 0.15s ease;
		font-size: 0.9375rem;
	}

	.nav-links a:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.nav-links a.active {
		background: var(--color-bg);
		color: var(--color-text);
		font-weight: 500;
	}

	.nav-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		opacity: 0.7;
	}

	.nav-links a.active .nav-icon {
		opacity: 1;
	}

	/* Open Apps Tabs */
	.open-apps {
		padding: 0 var(--space-md);
		margin-bottom: var(--space-md);
	}

	.open-apps-label {
		font-size: 0.6875rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		padding: 0 var(--space-md);
		margin-bottom: var(--space-xs);
	}

	.app-tabs {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	.app-tab-item {
		display: flex;
		align-items: center;
		margin-bottom: 2px;
		border-radius: var(--radius-md);
		transition: background 0.15s ease;
	}

	.app-tab-item:hover {
		background: var(--color-bg-subtle);
	}

	.app-tab-item:hover .close-tab {
		opacity: 1;
	}

	.app-tab {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		flex: 1;
		padding: var(--space-xs) var(--space-sm);
		background: transparent;
		border: none;
		border-radius: var(--radius-md);
		color: var(--color-text-secondary);
		font-family: var(--font-serif);
		font-size: 0.875rem;
		text-align: left;
		text-decoration: none;
		cursor: pointer;
		transition: color 0.15s ease;
	}

	.app-tab:hover {
		color: var(--color-text);
	}

	.app-tab.active {
		color: var(--color-text);
	}

	.app-tab-item:has(.app-tab.active) {
		background: var(--color-bg);
	}

	.app-tab-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		flex-shrink: 0;
	}

	.app-tab-icon :global(.app-icon) {
		width: 20px !important;
		height: 20px !important;
		border-radius: 4px;
	}

	.app-tab-name {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.close-tab {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		margin-right: var(--space-xs);
		padding: 0;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		opacity: 0;
		transition: all 0.1s ease;
		flex-shrink: 0;
	}

	.close-tab:hover {
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.sidebar-footer {
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border-subtle);
	}

	.user-section {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: var(--space-sm);
	}

	.username {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		color: var(--color-text-secondary);
		font-size: 0.8125rem;
		overflow: hidden;
	}

	.username-text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.logout-form {
		margin: 0;
	}

	.logout-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: var(--space-xs);
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.logout-btn:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.version {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono);
		text-decoration: none;
		transition: color 0.1s ease;
	}

	.version:hover {
		color: var(--color-text-secondary);
	}

	/* Collapsed sidebar - icons only */
	.sidebar.collapsed {
		width: 64px;
	}

	.sidebar.collapsed .logo {
		padding: var(--space-lg) var(--space-sm);
		text-align: center;
	}

	.sidebar.collapsed .nav-links {
		padding: var(--space-lg) var(--space-sm);
	}

	.sidebar.collapsed .nav-links a {
		justify-content: center;
		padding: var(--space-sm);
	}

	.sidebar.collapsed .nav-links a span:not(.nav-icon) {
		display: none;
	}

	.sidebar.collapsed .sidebar-footer {
		padding: var(--space-md) var(--space-sm);
	}

	.sidebar.collapsed .version {
		display: block;
		text-align: center;
	}

	.sidebar.collapsed .open-apps {
		padding: 0 var(--space-sm);
	}

	.sidebar.collapsed .open-apps-label {
		display: none;
	}

	.sidebar.collapsed .app-tab-item {
		justify-content: center;
	}

	.sidebar.collapsed .app-tab {
		justify-content: center;
		padding: var(--space-xs);
	}

	.sidebar.collapsed .app-tab-name {
		display: none;
	}

	.sidebar.collapsed .close-tab {
		display: none;
	}

	.sidebar.collapsed .user-section {
		flex-direction: column;
		gap: var(--space-xs);
	}

	.sidebar.collapsed .username-text {
		display: none;
	}

	.sidebar.collapsed .username {
		justify-content: center;
	}

	/* Mobile - always collapsed */
	@media (max-width: 768px) {
		.sidebar {
			width: 64px;
		}

		.logo {
			padding: var(--space-lg) var(--space-sm);
			text-align: center;
		}

		.logo-text {
			font-size: 1rem;
		}

		.nav-links {
			padding: var(--space-lg) var(--space-sm);
		}

		.nav-links a {
			justify-content: center;
			padding: var(--space-sm);
		}

		.nav-links a span:not(.nav-icon) {
			display: none;
		}

		.sidebar-footer {
			padding: var(--space-md) var(--space-sm);
		}

		.version {
			display: block;
			text-align: center;
		}

		/* Open apps - icon only */
		.open-apps {
			padding: 0 var(--space-sm);
		}

		.open-apps-label {
			display: none;
		}

		.app-tab-item {
			justify-content: center;
		}

		.app-tab {
			justify-content: center;
			padding: var(--space-xs);
		}

		.app-tab-name {
			display: none;
		}

		.close-tab {
			display: none;
		}

		.collapse-btn {
			display: none;
		}
	}
</style>
