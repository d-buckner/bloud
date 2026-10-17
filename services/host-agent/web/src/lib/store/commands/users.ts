// SPDX-License-Identifier: AGPL-3.0-only
/**
 * User operations - the admin list, shown before Authentik answers.
 *
 * The list is small and every change is local to it, so the overlay carries the
 * whole list after the change. A create appends a pending member, a delete
 * removes one, and a role change flips one flag. The confirmed read replaces the
 * claim as soon as it lands, so a pending member is never more than one pass old.
 *
 * A pending member carries `id: 0`: the server assigns the real one, and the
 * section keys its rows by username, so two creates in the same second cannot
 * collide on a key.
 */

import {
	createUser as apiCreate,
	deleteUser as apiDelete,
	fetchUsers,
	setUserRole as apiSetRole
} from '$lib/clients/userClient';
import type { ManagedUser } from '$lib/clients/userClient';
import type { Role } from '$lib/types';
import { settingsLoaded, settingsReadFailed, settingsReadStarted } from '../actions';
import { dispatch, getState } from '../kernel';
import { INSTANCE_KEY } from '../overlay';
import { commandKey } from '../reducers/commands';
import { messageOf, runCommand } from '../runner';
import type { CommandResult } from '../runner';

const READ_KEY = commandKey('read', 'settings:users');

const usersLoaded = (users: ManagedUser[]) => ({
	field: 'users' as const,
	value: users,
	at: Date.now()
});

/** The read the section calls on mount. */
export function loadUsers(): Promise<CommandResult<ManagedUser[]>> {
	return runCommand({
		key: READ_KEY,
		run: async () => {
			dispatch(settingsReadStarted({ field: 'users' }));
			try {
				const users = await fetchUsers();
				dispatch(settingsLoaded(usersLoaded(users)));
				return users;
			} catch (err) {
				dispatch(settingsReadFailed({ field: 'users', error: messageOf(err) }));
				throw err;
			}
		}
	});
}

async function refreshUsers(): Promise<void> {
	const users = await fetchUsers();
	dispatch(settingsLoaded(usersLoaded(users)));
}

/** The confirmed list, or an empty one before the first read. */
function confirmedUsers(): ManagedUser[] {
	return getState().settings.users.value ?? [];
}

function claim(users: ManagedUser[]): { users: ManagedUser[] } {
	return { users };
}

export function createUser(input: { username: string; password: string; role: Role }): Promise<CommandResult<void>> {
	const username = input.username.trim();
	const pending: ManagedUser = {
		id: 0,
		username,
		name: username,
		is_admin: input.role === 'admin',
		is_active: true
	};
	const next = [...confirmedUsers().filter((user) => user.username !== username), pending];
	return runCommand({
		key: commandKey('user', `create:${username}`),
		optimistic: { kind: 'settings.users', key: INSTANCE_KEY, value: claim(next) },
		failure: `Could not create ${username}`,
		unconfirmed: `${username} was created but is not in the list yet`,
		run: () => apiCreate({ username, password: input.password, role: input.role }),
		read: refreshUsers,
	});
}

export function deleteUser(username: string): Promise<CommandResult<void>> {
	const next = confirmedUsers().filter((user) => user.username !== username);
	return runCommand({
		key: commandKey('user', `delete:${username}`),
		optimistic: { kind: 'settings.users', key: INSTANCE_KEY, value: claim(next) },
		failure: `Could not delete ${username}`,
		unconfirmed: `${username} is still in the list`,
		run: () => apiDelete(username),
		read: refreshUsers,
	});
}

export function setUserRole(username: string, role: Role): Promise<CommandResult<void>> {
	const next = confirmedUsers().map((user) =>
		user.username === username ? { ...user, is_admin: role === 'admin' } : user
	);
	return runCommand({
		key: commandKey('user', `role:${username}`),
		optimistic: { kind: 'settings.users', key: INSTANCE_KEY, value: claim(next) },
		failure: `Could not change the role for ${username}`,
		unconfirmed: `The role for ${username} is not live yet`,
		run: () => apiSetRole(username, role),
		read: refreshUsers,
	});
}
