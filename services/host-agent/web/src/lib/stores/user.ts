// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { writable, derived } from 'svelte/store';

export type Role = 'admin' | 'member';

export interface CurrentUser {
	username: string;
	role: Role;
}

export const currentUser = writable<CurrentUser | null>(null);

export const isAdmin = derived(currentUser, ($user) => $user?.role === 'admin');
