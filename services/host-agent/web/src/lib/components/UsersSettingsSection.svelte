<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import {
		fetchUsers,
		createUser,
		deleteUser,
		setUserRole,
		type ManagedUser
	} from '$lib/clients/userClient';
	import { type Role } from '$lib/stores/user';

	let users = $state<ManagedUser[]>([]);
	let usersLoading = $state(true);
	let usersError = $state('');
	let creatingUser = $state(false);
	let newUsername = $state('');
	let newPassword = $state('');
	let newRole = $state<Role>('member');
	let deleteConfirm = $state<string | null>(null);

	onMount(loadUsers);

	async function loadUsers() {
		usersLoading = true;
		usersError = '';
		try {
			users = await fetchUsers();
		} catch (err: unknown) {
			usersError = errMessage(err, 'Failed to load users');
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
			usersError = errMessage(err, 'Failed to create user');
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
			usersError = errMessage(err, 'Failed to delete user');
		}
	}

	async function handleToggleRole(user: ManagedUser) {
		usersError = '';
		const newRoleValue: Role = user.is_admin ? 'member' : 'admin';
		try {
			await setUserRole(user.username, newRoleValue);
			await loadUsers();
		} catch (err: unknown) {
			usersError = errMessage(err, 'Failed to update role');
		}
	}

	function errMessage(err: unknown, fallback: string): string {
		if (err && typeof err === 'object' && 'message' in err) {
			return String((err as { message: unknown }).message);
		}
		return fallback;
	}
</script>

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
						<input id="new-username" type="text" bind:value={newUsername} required />
					</div>
					<div class="form-field">
						<label for="new-password">Password</label>
						<input id="new-password" type="password" bind:value={newPassword} required />
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

<style>
	/* The section shell matches the other settings sections exactly. Each
	   section carries its own copy because the page's scoped styles do not
	   reach into a child component's markup. */
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

	.form-row .form-field {
		flex: 1;
		min-width: 140px;
	}

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

	.error-message {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: rgba(220, 38, 38, 0.05);
		border: 1px solid rgba(220, 38, 38, 0.15);
		border-radius: var(--radius-md);
	}
</style>
