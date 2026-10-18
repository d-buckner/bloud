<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import DeleteUserModal from './DeleteUserModal.svelte';
	import Icon from './Icon.svelte';
	import { commands, select, usersView } from '$lib/store';
	import type { ManagedUser } from '$lib/clients/userClient';
	import type { Role } from '$lib/types';
	import { guardUserRow, demoteBlocked, deleteBlocked } from '$lib/utils/userGuard';

	const currentUser = select((s) => s.session.user);
	// The confirmed list, with the claim an add, a delete or a role change made
	// projected over it. A pending member carries id 0, so rows are keyed by
	// username.
	const users = select(usersView);

	let newUsername = $state('');
	let newPassword = $state('');
	let newRole = $state<Role>('member');
	// The user whose deletion is being confirmed, not the one already deleted:
	// the dialog has to name its target while it is still open.
	let pendingDelete = $state<string | null>(null);

	const list = $derived(users.current.value ?? []);
	const loading = $derived(
		users.current.phase === 'idle' ||
			(users.current.phase === 'loading' && users.current.value === null)
	);
	const loadError = $derived(users.current.phase === 'error' ? users.current.error : '');

	onMount(() => {
		void commands.loadUsers();
	});

	function rowGuard(u: ManagedUser) {
		return guardUserRow(u, list, currentUser.current?.username ?? null);
	}

	/**
	 * Clear the form at once, because the new row is already on screen. A failure
	 * comes back as a toast and the row leaves with it. Retyping a password after
	 * a visible failure is the safe direction: the form never holds a credential
	 * for an account that was not created.
	 */
	function handleCreateUser() {
		if (!newUsername || !newPassword) return;
		void commands.createUser({ username: newUsername, password: newPassword, role: newRole });
		newUsername = '';
		newPassword = '';
		newRole = 'member';
	}

	/** Close first, then let the claim remove the row. A failure is a toast. */
	function handleDeleteUser(username: string) {
		pendingDelete = null;
		void commands.deleteUser(username);
	}

	function handleToggleRole(user: ManagedUser) {
		void commands.setUserRole(user.username, user.is_admin ? 'member' : 'admin');
	}
</script>

<section class="section users-section">
	<h2>Users</h2>
	<p class="section-description">
		Manage users who can access this Bloud instance. Admins can install apps and manage settings.
	</p>

	{#if loading}
		<div class="loading-state"><p>Loading users...</p></div>
	{:else}
		{#if list.length > 0}
			<div class="users-list">
				{#each list as u (u.username)}
					{@const guard = rowGuard(u)}
					<div class="user-row">
						<div class="user-line">
							<div class="user-info">
								<span class="user-name">{u.username}</span>
								<span class="role-badge" class:admin={u.is_admin}>
									{u.is_admin ? 'Admin' : 'Member'}
								</span>
							</div>
							<div class="user-controls">
								<button
									class="btn-sm"
									onclick={() => handleToggleRole(u)}
									title={guard.reason || (u.is_admin ? 'Demote to member' : 'Promote to admin')}
									aria-describedby={guard.reason ? `guard-${u.username}` : undefined}
									disabled={demoteBlocked(guard)}
								>
									{u.is_admin ? 'Make Member' : 'Make Admin'}
								</button>
								<span class="danger-group">
									<button
										class="btn-sm btn-sm-danger"
										onclick={() => (pendingDelete = u.username)}
										title={guard.reason || `Delete ${u.username}`}
										aria-describedby={guard.reason ? `guard-${u.username}` : undefined}
										disabled={deleteBlocked(guard)}
									>
										Delete
									</button>
								</span>
							</div>
						</div>
						{#if guard.reason}
							<p class="guard-note" id={`guard-${u.username}`}>
								<Icon name="info" size={13} />
								{guard.reason}
							</p>
						{/if}
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
						<input
							id="new-username"
							type="text"
							autocomplete="username"
							autocapitalize="none"
							bind:value={newUsername}
							required
						/>
					</div>
					<div class="form-field">
						<label for="new-password">Password</label>
						<!-- new-password, not off: a password manager will offer to generate
						     here, which is the right answer for a credential the operator is
						     creating for someone else and will never type twice. -->
						<input
							id="new-password"
							type="password"
							autocomplete="new-password"
							bind:value={newPassword}
							required
						/>
					</div>
					<div class="form-field">
						<label for="new-role">Role</label>
						<select id="new-role" bind:value={newRole}>
							<option value="member">Member</option>
							<option value="admin">Admin</option>
						</select>
					</div>
				</div>
				<button class="btn btn-primary" type="submit" disabled={!newUsername.trim() || !newPassword}>
					Create User
				</button>
			</form>
		</div>
	{/if}

	{#if loadError}
		<div class="error-message" role="alert">{loadError}</div>
	{/if}
</section>

<DeleteUserModal
	username={pendingDelete}
	onclose={() => (pendingDelete = null)}
	onconfirm={handleDeleteUser}
/>

<style>
	/* The list and the Add User form sit side by side once there is room, so a
	   full-width section does not stretch one list across the whole page. The
	   heading and any error span both columns. */
	.users-section {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 340px), 1fr));
		gap: var(--space-md) var(--space-xl);
		align-items: start;
	}

	.users-section > h2,
	.users-section > .section-description,
	.users-section > .error-message {
		grid-column: 1 / -1;
	}

	/* The section heading size comes from `base.css`, where every settings section
	   reads the same rule. */
	.section-description {
		margin: 0 0 var(--space-md) 0;
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		line-height: 1.5;
	}

	.loading-state {
		padding: var(--space-xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	/* The settings grid owns the spacing between sections, so this one carries no
	   rule of its own. */
	.users-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-bottom: var(--space-md);
	}

	.user-row {
		padding: var(--space-sm) var(--space-md);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.user-line {
		display: flex;
		align-items: center;
		justify-content: space-between;
		/* Wrapped rather than squeezed: the row's min-content is what sets the
		   page width on a narrow viewport, and a nowrap pair of control groups
		   would widen every route that renders it. */
		flex-wrap: wrap;
		gap: var(--space-sm) var(--space-md);
	}

	.user-info {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		min-width: 0;
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
		color: var(--color-on-accent);
	}

	.user-controls {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	/* The destructive control gets its own group behind a divider. Two buttons
	   of the same chrome, three pixels apart, is how the last-admin foot-gun
	   read as a pair of ordinary row actions. */
	.danger-group {
		display: inline-flex;
		align-items: center;
		margin-left: var(--space-xs);
		padding-left: var(--space-sm);
		border-left: 1px solid var(--color-border);
	}

	.guard-note {
		display: flex;
		align-items: center;
		gap: 6px;
		margin: var(--space-sm) 0 0 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
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

	.btn-sm:hover:not(:disabled) {
		background: var(--color-bg-elevated);
		color: var(--color-text);
	}

	.btn-sm:disabled {
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		border-color: var(--color-border);
		cursor: not-allowed;
		opacity: 1;
	}

	.btn-sm-danger {
		color: var(--color-error);
		border-color: color-mix(in srgb, var(--color-error) 45%, transparent);
	}

	.btn-sm-danger:hover:not(:disabled) {
		background: var(--color-danger);
		color: var(--color-on-danger);
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
				border-color: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-text) 12%, transparent);
	}

	.form-field input::placeholder {
		color: var(--color-text-muted);
	}

	.form-row .form-field {
		flex: 1;
		min-width: 0;
	}

	/* The Add User row is three fields. At 390px three columns are unreadable and
	   the row is what pushed the page wider than the screen, so below 640px it is
   	one field per line. */
	@media (max-width: 640px) {
		.form-row {
			flex-direction: column;
		}
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
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
		border-color: var(--color-border);
		cursor: not-allowed;
		opacity: 1;
	}

	.btn-primary {
		background: var(--color-accent);
		color: var(--color-on-accent);
	}

	.btn-primary:hover:not(:disabled) {
		opacity: 0.9;
	}

	.error-message {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 0.875rem;
		color: var(--color-error);
		background: var(--color-error-bg);
		border: 1px solid color-mix(in srgb, var(--color-error) 30%, transparent);
		border-radius: var(--radius-md);
	}
</style>
