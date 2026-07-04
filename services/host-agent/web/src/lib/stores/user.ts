import { writable, derived } from 'svelte/store';

export type Role = 'admin' | 'member';

export interface CurrentUser {
	username: string;
	role: Role;
}

export const currentUser = writable<CurrentUser | null>(null);

export const isAdmin = derived(currentUser, ($user) => $user?.role === 'admin');
