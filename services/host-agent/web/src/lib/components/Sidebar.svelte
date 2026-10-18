<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import Icon from './Icon.svelte';
	import ThemeToggle from './ThemeToggle.svelte';
	import { gridApps, isAdmin, select } from '$lib/store';
	import { AppStatus } from '$lib/types';
	import { resolve } from '$app/paths';
	import type { RouteId } from '$app/types';

	interface Props {
		/** Desktop rail collapsed to icons. Ignored below the drawer breakpoint. */
		collapsed?: boolean;
		/** Mobile drawer open. The layout owns the value and the scrim. */
		open?: boolean;
		/** Called when a destination is chosen, so the layout can close the drawer. */
		onclose?: () => void;
	}

	let { collapsed = $bindable(false), open = false, onclose }: Props = $props();

	const STORAGE_KEY = 'bloud.sidebar.collapsed';

	// The rail remembers its width. It was a bindable prop with an expand button
	// and a full set of collapsed styles, but nothing ever set it to true, so the
	// collapsed state was unreachable. A control plus this read is what makes the
	// feature real.
	onMount(() => {
		collapsed = localStorage.getItem(STORAGE_KEY) === '1';
	});

	function toggleCollapsed() {
		collapsed = !collapsed;
		localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0');
	}

	let currentPath = $derived(page.url.pathname);
	// `/catalog` is the dashboard with the catalog modal over it, so Home stays
	// the active destination while it is open.
	let activePath = $derived(currentPath === '/catalog' ? '/' : currentPath);
	const user = select((s) => s.session.user);
	const admin = select(isAdmin);
	const installed = select(gridApps);

	interface NavItem {
		href: RouteId;
		label: string;
		icon: string;
		adminOnly: boolean;
	}

	// The catalog is a modal opened from the dashboard, so it is not a nav
	// destination. A nav item that opens an overlay instead of a page does not
	// behave like its siblings.
	const allNavItems: NavItem[] = [
		{ href: '/', label: 'Home', icon: 'home', adminOnly: false },
		{ href: '/settings', label: 'Settings', icon: 'settings', adminOnly: true },
		{ href: '/developer', label: 'Developer', icon: 'terminal', adminOnly: true }
	];

	let navItems = $derived(admin.current ? allNavItems : allNavItems.filter((item) => !item.adminOnly));

	// Apps that need a look: install gave up (failed) or degraded (error).
	let attentionApps = $derived(
		installed.current.filter((a) => a.status === AppStatus.Failed || a.status === AppStatus.Error)
	);
</script>

<nav class="sidebar" class:collapsed class:open aria-label="Main navigation">
	<div class="sidebar-header">
		<span class="logo-text">Bloud</span>
		<button
			class="collapse-toggle"
			type="button"
			aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
			aria-expanded={!collapsed}
			title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
			onclick={toggleCollapsed}
		>
			<Icon name={collapsed ? 'menu' : 'chevron-left'} size={18} />
		</button>
		<button
			class="close-toggle"
			type="button"
			aria-label="Close navigation"
			onclick={() => onclose?.()}
		>
			<Icon name="close" size={18} />
		</button>
	</div>

	<ul class="nav-links">
		{#each navItems as item (item.href)}
			<li>
				<a
					href={resolve(item.href)}
					class:active={activePath === item.href}
					aria-current={activePath === item.href ? 'page' : undefined}
					title={item.label}
					onclick={() => onclose?.()}
				>
					<span class="nav-icon">
						<Icon name={item.icon} size={20} />
					</span>
					<span class="nav-label">{item.label}</span>
					{#if item.href === '/' && attentionApps.length > 0}
						<span
							class="attention-chip"
							title={attentionApps.map((a) => a.display_name).join(', ')}
							aria-label={attentionApps.length === 1
								? '1 app needs attention'
								: `${attentionApps.length} apps need attention`}
						>
							<Icon name="warning" size={12} />
							<span class="attention-text">{attentionApps.length} need attention</span>
						</span>
					{/if}
				</a>
			</li>
		{/each}
	</ul>

	<div class="sidebar-footer">
		{#if user.current}
			<span class="username" title={user.current.username}>
				<Icon name="user" size={16} />
				<span class="username-text">{user.current.username}</span>
			</span>
		{/if}
		<div class="footer-actions">
			<ThemeToggle {collapsed} />
			<form action="/auth/logout" method="POST" class="logout-form" data-sveltekit-reload>
				<button
					type="submit"
					class="logout-btn"
					title="Sign out"
					aria-label="Sign out"
				>
					<Icon name="logout" size={16} />
				</button>
			</form>
		</div>
	</div>
</nav>

<style>
	.sidebar {
		width: var(--sidebar-width);
		/* A well, not a lift: the rail is a distinct surface from the canvas
		   without depending on the hairline border alone, and the active row can
		   then be a lift on top of it. */
		background: var(--color-bg-subtle);
		border-right: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		position: fixed;
		top: 0;
		left: 0;
		bottom: 0;
		z-index: 80;
		transition: width 0.2s ease, transform 0.2s ease;
	}

	.sidebar-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
		padding: var(--space-md);
		border-bottom: 1px solid var(--color-border-subtle);
	}

	.logo-text {
		font-size: 1.25rem;
		font-weight: 500;
		letter-spacing: -0.02em;
	}

	.collapse-toggle,
	.close-toggle {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: var(--tap-target-min);
		height: var(--tap-target-min);
		padding: 0;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		transition: background 0.15s ease, color 0.15s ease;
	}

	.collapse-toggle:hover,
	.close-toggle:hover {
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.close-toggle {
		display: none;
	}

	.nav-links {
		list-style: none;
		margin: 0;
		padding: var(--space-sm);
		flex: 1;
	}

	.nav-links li {
		margin-bottom: var(--space-xs);
	}

	.nav-links a {
		position: relative;
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius-md);
		text-decoration: none;
		color: var(--color-text-secondary);
		transition: background 0.15s ease, color 0.15s ease;
		font-size: 0.9375rem;
		/* A nav link is an <a>, so the global button rule does not reach it, and
		   the audit measured these at 43px: one pixel short of the minimum, which
		   is the kind of miss that only a rule catches. */
		min-height: var(--tap-target-min);
	}

	.nav-links a:hover {
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.nav-links a.active {
		background: var(--color-bg-elevated);
		color: var(--color-text);
		font-weight: 600;
	}

	/* The active row also carries a bar on its leading edge. The fill alone was
	   within a hair of the rail's own value, so the current page was a squint. */
	.nav-links a.active::before {
		content: '';
		position: absolute;
		left: 0;
		top: 50%;
		transform: translateY(-50%);
		width: 3px;
		height: 60%;
		border-radius: 0 2px 2px 0;
		background: var(--color-accent);
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

	.sidebar-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
		padding: var(--space-md);
		border-top: 1px solid var(--color-border-subtle);
	}

	.username {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		min-width: 0;
		color: var(--color-text-secondary);
		font-size: 0.8125rem;
	}

	.username-text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.footer-actions {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		flex-shrink: 0;
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
		transition: background 0.15s ease, color 0.15s ease;
	}

	.logout-btn:hover {
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	/* --- Collapsed rail, desktop only ---------------------------------------

	   The collapsed rules are scoped to the desktop width on purpose. Below the
	   drawer breakpoint the rail is off-canvas and always shows its labels, so a
	   persisted collapsed state cannot strip the names off a phone's navigation. */
	@media (min-width: 769px) {
		.sidebar.collapsed {
			width: 64px;
		}

		.sidebar.collapsed .sidebar-header {
			justify-content: center;
			padding: var(--space-sm);
		}

		.sidebar.collapsed .logo-text {
			display: none;
		}

		.sidebar.collapsed .nav-links {
			padding: var(--space-sm);
		}

		.sidebar.collapsed .nav-links a {
			justify-content: center;
			padding: var(--space-sm);
		}

		.sidebar.collapsed .nav-links a span:not(.nav-icon),
		.sidebar.collapsed .username-text {
			/* Hidden visually, never removed: an icon rail whose links are
			   `display: none` apart from their icon is a row of anonymous "link"
			   announcements. The `title` on each link is what a pointer user gets. */
			position: absolute;
			width: 1px;
			height: 1px;
			margin: -1px;
			padding: 0;
			overflow: hidden;
			clip-path: inset(50%);
			white-space: nowrap;
			border: 0;
		}

		.sidebar.collapsed .sidebar-footer {
			justify-content: center;
			padding: var(--space-sm);
		}

		.sidebar.collapsed .footer-actions {
			flex-direction: column;
		}
	}

	/* --- Off-canvas drawer, narrow screens ---------------------------------- */
	@media (max-width: 768px) {
		.sidebar {
			width: 264px;
			transform: translateX(-100%);
			box-shadow: var(--shadow-popover);
		}

		.sidebar.open {
			transform: none;
		}

		.collapse-toggle {
			display: none;
		}

		.close-toggle {
			display: inline-flex;
		}
	}
</style>
