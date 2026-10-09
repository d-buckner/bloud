// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import type { ManagedUser } from '$lib/clients/userClient';
import { deleteBlocked, demoteBlocked, guardUserRow } from '../userGuard';

function user(username: string, isAdmin: boolean): ManagedUser {
	return { id: username.length, username, name: username, is_admin: isAdmin, is_active: true };
}

const admin = user('daniel', true);
const member = user('sam', false);
const otherAdmin = user('robin', true);

describe('guardUserRow', () => {
	it('leaves an ordinary row unguarded', () => {
		const guard = guardUserRow(member, [admin, member], 'daniel');
		expect(guard).toEqual({ lastAdmin: false, isSelf: false, reason: '' });
	});

	it('flags the only admin when somebody else is around', () => {
		const guard = guardUserRow(admin, [admin, member], 'robin');
		expect(guard.lastAdmin).toBe(true);
		expect(guard.reason).toContain('only admin');
	});

	it('does not flag an admin while a second admin exists', () => {
		const guard = guardUserRow(admin, [admin, otherAdmin, member], 'robin');
		expect(guard.lastAdmin).toBe(false);
		expect(guard.reason).toBe('');
	});

	it('flags the account the operator is signed in as', () => {
		const guard = guardUserRow(admin, [admin, otherAdmin], 'daniel');
		expect(guard.isSelf).toBe(true);
		expect(guard.lastAdmin).toBe(false);
		expect(guard.reason).toContain('signed in as');
	});

	it('says both when the operator is the only admin', () => {
		const guard = guardUserRow(admin, [admin, member], 'daniel');
		expect(guard.lastAdmin).toBe(true);
		expect(guard.isSelf).toBe(true);
		expect(guard.reason).toContain('signed in as');
		expect(guard.reason).toContain('only admin');
	});

	it('tolerates an unknown operator', () => {
		expect(guardUserRow(admin, [admin], null).isSelf).toBe(false);
	});
});

describe('demoteBlocked', () => {
	it('blocks making the last member of the admins group', () => {
		expect(demoteBlocked(guardUserRow(admin, [admin, member], 'robin'))).toBe(true);
	});

	it('blocks demoting yourself when you are the only admin', () => {
		expect(demoteBlocked(guardUserRow(admin, [admin, member], 'daniel'))).toBe(true);
	});

	it('allows demoting yourself while another admin is left to undo it', () => {
		expect(demoteBlocked(guardUserRow(admin, [admin, otherAdmin], 'daniel'))).toBe(false);
	});

	it('allows a demotion that leaves an admin behind', () => {
		expect(demoteBlocked(guardUserRow(admin, [admin, otherAdmin], 'robin'))).toBe(false);
	});

	it('never blocks a promotion', () => {
		expect(demoteBlocked(guardUserRow(member, [admin], 'robin'))).toBe(false);
	});
});

describe('deleteBlocked', () => {
	it('blocks deleting the last admin', () => {
		expect(deleteBlocked(guardUserRow(admin, [admin, member], 'robin'))).toBe(true);
	});

	it('blocks deleting your own account even with another admin present', () => {
		expect(deleteBlocked(guardUserRow(admin, [admin, otherAdmin], 'daniel'))).toBe(true);
	});

	it('allows a member and a non-last admin', () => {
		expect(deleteBlocked(guardUserRow(member, [admin, member], 'robin'))).toBe(false);
		expect(deleteBlocked(guardUserRow(admin, [admin, otherAdmin], 'robin'))).toBe(false);
	});
});
