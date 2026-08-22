<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { page } from '$app/state';
	import Icon from './Icon.svelte';
	import { isAdmin } from '$lib/stores/user';
	import { visibleApps } from '$lib/stores/apps';
	import { AppStatus } from '$lib/types';

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

	const allNavItems = [
		{ href: '/', label: 'Apps', icon: 'home', adminOnly: false },
		{ href: '/catalog', label: 'Catalog', icon: 'store', adminOnly: false },
		{ href: '/settings', label: 'Settings', icon: 'settings', adminOnly: true },
		{ href: '/community', label: 'Community', icon: 'users', adminOnly: true },
		{ href: '/developer', label: 'Developer', icon: 'terminal', adminOnly: true }
	];

	let navItems = $derived(
		$isAdmin ? allNavItems : allNavItems.filter((item) => !item.adminOnly)
	);

	// Apps that need a look: install gave up (failed) or degraded (error).
	let attentionApps = $derived(
		$visibleApps.filter((a) => a.status === AppStatus.Failed || a.status === AppStatus.Error)
	);
</script>

<nav class="sidebar" class:collapsed>
	<div class="logo">
		{#if collapsed}
			<button class="expand-btn" onclick={() => (collapsed = false)} title="Expand sidebar">
				<Icon name="menu" size={20} />
			</button>
		{:else}
			<span class="logo-text">Bloud</span>
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
					{#if item.href === '/' && attentionApps.length > 0}
						<span
							class="attention-chip"
							title={attentionApps.map((a) => a.display_name).join(', ')}
						>
							<Icon name="warning" size={12} />
							{#if !collapsed}
								<span class="attention-text">{attentionApps.length} need attention</span>
							{/if}
						</span>
					{/if}
				</a>
			</li>
		{/each}
	</ul>

	<div class="sidebar-footer">
		{#if user}
			<div class="user-section">
				<span class="username" title={user.username}>
					<Icon name="user" size={16} />
					<span class="username-text">{user.username}</span>
				</span>
				<form action="/auth/logout" method="POST" class="logout-form" data-sveltekit-reload>
					<button type="submit" class="logout-btn" title="Sign out">
						<Icon name="logout" size={16} />
					</button>
				</form>
			</div>
		{/if}
		<span class="version">v0.1.0</span>
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

	.expand-btn {
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
		margin: 0 auto;
	}

	.expand-btn:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
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

	.attention-chip {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		margin-left: auto;
		padding: 2px 8px;
		border-radius: 10px;
		background: var(--color-error-bg);
		color: var(--color-error);
		font-size: 0.6875rem;
		font-weight: 600;
		white-space: nowrap;
	}

	.attention-text {
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.nav-links a.active .nav-icon {
		opacity: 1;
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
	}
</style>
