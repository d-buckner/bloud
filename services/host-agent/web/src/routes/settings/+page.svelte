<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import {
		fetchTailnet,
		setTailnet,
		deleteTailnet,
		type TailnetConnection
	} from '$lib/clients/settingsClient';
	import {
		fetchUsers,
		createUser,
		deleteUser,
		setUserRole,
		type ManagedUser
	} from '$lib/clients/userClient';
	import { isAdmin, type Role } from '$lib/stores/user';

	let connection = $state<TailnetConnection | null>(null);
	let loading = $state(true);
	let error = $state('');
	let saving = $state(false);

	// Users state
	let users = $state<ManagedUser[]>([]);
	let usersLoading = $state(true);
	let usersError = $state('');
	let creatingUser = $state(false);
	let newUsername = $state('');
	let newPassword = $state('');
	let newRole = $state<Role>('member');
	let deleteConfirm = $state<string | null>(null);

	// Form state
	let formName = $state('');
	let formType = $state<'tailscale' | 'headscale'>('tailscale');
	let formAuthKey = $state('');
	let formControlUrl = $state('');

	const POLL_INTERVAL = 500;
	const POLL_TIMEOUT = 10_000;

	async function pollTailnet(
		predicate: (conn: TailnetConnection | null) => boolean
	): Promise<TailnetConnection | null> {
		const deadline = Date.now() + POLL_TIMEOUT;
		while (Date.now() < deadline) {
			const result = await fetchTailnet();
			if (predicate(result)) return result;
			await new Promise((r) => setTimeout(r, POLL_INTERVAL));
		}
		throw new Error('Timed out waiting for tailnet update');
	}

	onMount(async () => {
		// Redirect non-admins
		if (!$isAdmin) {
			goto('/');
			return;
		}

		try {
			connection = await fetchTailnet();
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to load settings';
		} finally {
			loading = false;
		}

		loadUsers();
	});

	async function loadUsers() {
		usersLoading = true;
		usersError = '';
		try {
			users = await fetchUsers();
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to load users';
			usersError = msg;
		} finally {
			usersLoading = false;
		}
	}

	async function handleCreateUser() {
		if (!newUsername || !newPassword) return;
		usersError = '';
		creatingUser = true;
		try {
			await createUser({ username: newUsername, password: newPassword, role: newRole });
			newUsername = '';
			newPassword = '';
			newRole = 'member';
			await loadUsers();
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to create user';
			usersError = msg;
		} finally {
			creatingUser = false;
		}
	}

	async function handleDeleteUser(username: string) {
		usersError = '';
		try {
			await deleteUser(username);
			deleteConfirm = null;
			await loadUsers();
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to delete user';
			usersError = msg;
		}
	}

	async function handleToggleRole(user: ManagedUser) {
		usersError = '';
		const newRoleValue: Role = user.is_admin ? 'member' : 'admin';
		try {
			await setUserRole(user.username, newRoleValue);
			await loadUsers();
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to update role';
			usersError = msg;
		}
	}

	async function handleSave() {
		error = '';
		saving = true;
		try {
			await setTailnet({
				name: formName,
				type: formType,
				authKey: formAuthKey,
				controlUrl: formType === 'headscale' ? formControlUrl : undefined
			});
			// Poll until the connection appears.
			connection = await pollTailnet((conn) => conn !== null);
			// Clear form on success
			formName = '';
			formAuthKey = '';
			formControlUrl = '';
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to save';
			error = msg;
		} finally {
			saving = false;
		}
	}

	async function handleRemove() {
		error = '';
		saving = true;
		try {
			await deleteTailnet();
			// Poll until the connection is gone.
			connection = await pollTailnet((conn) => conn === null);
		} catch (err: unknown) {
			const msg = err && typeof err === 'object' && 'message' in err
				? (err as { message: string }).message
				: 'Failed to remove';
			error = msg;
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>Settings · Bloud</title>
</svelte:head>

<div class="page">
	<header class="page-header">
		<div class="header-content">
			<h1>Settings</h1>
			<p class="subtitle">Configure your Bloud instance</p>
		</div>
	</header>

	<section class="section">
		<h2>Tailnet Connection</h2>
		<p class="section-description">
			Connect to a Tailscale or Headscale network to enable app sharing with other Bloud users.
		</p>

		{#if loading}
			<div class="loading-state">
				<p>Loading...</p>
			</div>
		{:else if connection}
			<div class="connection-card">
				<div class="connection-info">
					<div class="field">
						<span class="field-label">Name</span>
						<span class="field-value">{connection.name}</span>
					</div>
					<div class="field">
						<span class="field-label">Type</span>
						<span class="field-value type-badge">{connection.type}</span>
					</div>
					<div class="field">
						<span class="field-label">Auth Key</span>
						<span class="field-value mono">{connection.hasAuthKey ? 'Configured' : 'Not set'}</span>
					</div>
					{#if connection.controlUrl}
						<div class="field">
							<span class="field-label">Control URL</span>
							<span class="field-value mono">{connection.controlUrl}</span>
						</div>
					{/if}
				</div>
				<button class="btn btn-danger" onclick={handleRemove} disabled={saving}>
					{saving ? 'Removing...' : 'Remove'}
				</button>
			</div>
		{:else}
			<form class="tailnet-form" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
				<div class="form-field">
					<label for="tn-name">Connection Name</label>
					<input
						id="tn-name"
						type="text"
						placeholder="e.g. My Tailnet"
						bind:value={formName}
						required
					/>
				</div>

				<div class="form-field">
					<label for="tn-type">Type</label>
					<select id="tn-type" bind:value={formType}>
						<option value="tailscale">Tailscale</option>
						<option value="headscale">Headscale</option>
					</select>
				</div>

				<div class="form-field">
					<label for="tn-authkey">Auth Key</label>
					<input
						id="tn-authkey"
						type="password"
						placeholder="tskey-auth-..."
						bind:value={formAuthKey}
						required
					/>
				</div>

				{#if formType === 'headscale'}
					<div class="form-field">
						<label for="tn-controlurl">Control URL</label>
						<input
							id="tn-controlurl"
							type="url"
							placeholder="https://hs.example.com"
							bind:value={formControlUrl}
							required
						/>
					</div>
				{/if}

				<button class="btn btn-primary" type="submit" disabled={saving}>
					{saving ? 'Saving...' : 'Save'}
				</button>
			</form>
		{/if}

		{#if error}
			<div class="error-message">{error}</div>
		{/if}
	</section>

	<section class="section users-section">
		<h2>Users</h2>
		<p class="section-description">
			Manage users who can access this Bloud instance. Admins can install apps and manage settings.
		</p>

		{#if usersLoading}
			<div class="loading-state"><p>Loading users...</p></div>
		{:else}
			{#if users.length > 0}
				<div class="users-list">
					{#each users as u (u.id)}
						<div class="user-row">
							<div class="user-info">
								<span class="user-name">{u.username}</span>
								<span class="role-badge" class:admin={u.is_admin}>
									{u.is_admin ? 'Admin' : 'Member'}
								</span>
							</div>
							<div class="user-actions">
								<button
									class="btn-sm"
									onclick={() => handleToggleRole(u)}
									title={u.is_admin ? 'Demote to member' : 'Promote to admin'}
								>
									{u.is_admin ? 'Make Member' : 'Make Admin'}
								</button>
								{#if deleteConfirm === u.username}
									<button class="btn-sm btn-sm-danger" onclick={() => handleDeleteUser(u.username)}>
										Confirm
									</button>
									<button class="btn-sm" onclick={() => (deleteConfirm = null)}>
										Cancel
									</button>
								{:else}
									<button class="btn-sm btn-sm-danger" onclick={() => (deleteConfirm = u.username)}>
										Delete
									</button>
								{/if}
							</div>
						</div>
					{/each}
				</div>
			{:else}
				<p class="empty-users">No users found.</p>
			{/if}

			<div class="create-user-form">
				<h3>Add User</h3>
				<form onsubmit={(e) => { e.preventDefault(); handleCreateUser(); }}>
					<div class="form-row">
						<div class="form-field">
							<label for="new-username">Username</label>
							<input id="new-username" type="text" bind:value={newUsername} placeholder="username" required />
						</div>
						<div class="form-field">
							<label for="new-password">Password</label>
							<input id="new-password" type="password" bind:value={newPassword} placeholder="password" required />
						</div>
						<div class="form-field">
							<label for="new-role">Role</label>
							<select id="new-role" bind:value={newRole}>
								<option value="member">Member</option>
								<option value="admin">Admin</option>
							</select>
						</div>
					</div>
					<button class="btn btn-primary" type="submit" disabled={creatingUser}>
						{creatingUser ? 'Creating...' : 'Create User'}
					</button>
				</form>
			</div>
		{/if}

		{#if usersError}
			<div class="error-message">{usersError}</div>
		{/if}
	</section>
</div>

<style>
	.page {
		padding: var(--space-2xl) var(--space-xl);
	}

	.page-header {
		margin-bottom: var(--space-2xl);
		padding-bottom: var(--space-xl);
		border-bottom: 1px solid var(--color-border);
	}

	.header-content h1 {
		margin: 0;
		font-size: 1.75rem;
		font-weight: 500;
	}

	.subtitle {
		margin: var(--space-xs) 0 0 0;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.section {
		max-width: 560px;
	}

	.section h2 {
		margin: 0 0 var(--space-xs) 0;
		font-size: 1.125rem;
		font-weight: 500;
	}

	.section-description {
		margin: 0 0 var(--space-xl) 0;
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		line-height: 1.5;
	}

	.loading-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	/* Connection display */
	.connection-card {
		padding: var(--space-lg);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.connection-info {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-bottom: var(--space-lg);
	}

	.field {
		display: flex;
		align-items: baseline;
		gap: var(--space-md);
	}

	.field-label {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		min-width: 80px;
	}

	.field-value {
		font-size: 0.9375rem;
		color: var(--color-text);
	}

	.field-value.mono {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
	}

	.type-badge {
		text-transform: capitalize;
	}

	/* Form */
	.tailnet-form {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.form-field label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.form-field input,
	.form-field select {
		padding: var(--space-sm) var(--space-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
		transition: border-color 0.15s ease;
	}

	.form-field input:focus,
	.form-field select:focus {
		outline: none;
		border-color: var(--color-accent);
		box-shadow: 0 0 0 3px rgba(28, 25, 23, 0.08);
	}

	.form-field input::placeholder {
		color: var(--color-text-muted);
	}

	/* Buttons */
	.btn {
		padding: var(--space-sm) var(--space-lg);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		cursor: pointer;
		transition: all 0.15s ease;
		align-self: flex-start;
	}

	.btn:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.btn-primary {
		background: var(--color-accent);
		color: white;
	}

	.btn-primary:hover:not(:disabled) {
		opacity: 0.9;
	}

	.btn-danger {
		background: transparent;
		border-color: var(--color-error);
		color: var(--color-error);
	}

	.btn-danger:hover:not(:disabled) {
		background: var(--color-error);
		color: white;
	}

	/* Error */
	.error-message {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: rgba(220, 38, 38, 0.05);
		border: 1px solid rgba(220, 38, 38, 0.15);
		border-radius: var(--radius-md);
	}

	/* Users section */
	.users-section {
		margin-top: var(--space-2xl);
		padding-top: var(--space-2xl);
		border-top: 1px solid var(--color-border);
	}

	.users-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-bottom: var(--space-xl);
	}

	.user-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.user-info {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.user-name {
		font-size: 0.9375rem;
		font-weight: 500;
	}

	.role-badge {
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: 9999px;
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
	}

	.role-badge.admin {
		background: var(--color-accent);
		color: white;
	}

	.user-actions {
		display: flex;
		gap: var(--space-xs);
	}

	.btn-sm {
		padding: 4px 10px;
		font-family: var(--font-serif);
		font-size: 0.8125rem;
		background: var(--color-bg-subtle);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.btn-sm:hover {
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.btn-sm-danger {
		color: var(--color-error);
		border-color: rgba(220, 38, 38, 0.3);
	}

	.btn-sm-danger:hover {
		background: var(--color-error);
		color: white;
	}

	.empty-users {
		color: var(--color-text-muted);
		font-style: italic;
		margin-bottom: var(--space-xl);
	}

	.create-user-form {
		margin-top: var(--space-lg);
	}

	.create-user-form h3 {
		margin: 0 0 var(--space-md) 0;
		font-size: 0.9375rem;
		font-weight: 500;
	}

	.form-row {
		display: flex;
		gap: var(--space-md);
		margin-bottom: var(--space-md);
		flex-wrap: wrap;
	}

	.form-row .form-field {
		flex: 1;
		min-width: 140px;
	}
</style>
