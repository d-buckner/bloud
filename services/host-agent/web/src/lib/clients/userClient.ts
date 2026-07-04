import { get, post, put, del } from './httpClient';
import type { Role } from '$lib/stores/user';

export interface ManagedUser {
	id: number;
	username: string;
	name: string;
	is_admin: boolean;
	is_active: boolean;
}

interface UsersResponse {
	users: ManagedUser[];
}

interface CreateUserRequest {
	username: string;
	password: string;
	role: Role;
}

interface SetRoleRequest {
	role: Role;
}

export async function fetchUsers(): Promise<ManagedUser[]> {
	const res = await get<UsersResponse>('/api/users');
	return res.users ?? [];
}

export async function createUser(data: CreateUserRequest): Promise<void> {
	await post('/api/users', data);
}

export async function deleteUser(username: string): Promise<void> {
	await del(`/api/users/${encodeURIComponent(username)}`);
}

export async function setUserRole(username: string, role: Role): Promise<void> {
	await put<SetRoleRequest>(`/api/users/${encodeURIComponent(username)}/role`, { role });
}
