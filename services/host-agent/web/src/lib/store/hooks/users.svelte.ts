// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The user list hook.
 *
 * The list, whether there is anything to draw, what a read failure said, and the
 * three writes. The create form's own fields stay in the component, because they
 * are not derived from the store and nothing else reads them.
 */

import { onMount } from 'svelte';
import type { ManagedUser } from '$lib/clients/userClient';
import type { Role } from '$lib/types';
import { select } from '../bindings.svelte';
import { commands } from '../commands';
import { isLoading, readError } from '../field.svelte';
import { usersView } from '../selectors';

export interface UsersList {
	readonly list: ManagedUser[];
	readonly loading: boolean;
	readonly error: string;
	create(input: { username: string; password: string; role: Role }): void;
	remove(username: string): void;
	setRole(username: string, role: Role): void;
}

export function useUsers(): UsersList {
	const view = select(usersView);
	onMount(() => {
		void commands.loadUsers();
	});

	return {
		get list(): ManagedUser[] {
			return view.current.value ?? [];
		},
		get loading(): boolean {
			return isLoading(view.current);
		},
		get error(): string {
			return readError(view.current);
		},
		create(input): void {
			void commands.createUser(input);
		},
		remove(username): void {
			void commands.deleteUser(username);
		},
		setRole(username, role): void {
			void commands.setUserRole(username, role);
		}
	};
}
