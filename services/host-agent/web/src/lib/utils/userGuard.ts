// SPDX-License-Identifier: AGPL-3.0-only
import type { ManagedUser } from '$lib/clients/userClient';

/**
 * Why the destructive controls on one user row are locked.
 *
 * The API already refuses two of these calls, deleting your own account and
 * demoting the only admin, so nothing here is the safety mechanism. What the API
 * cannot do is stop the operator from reaching for the control: a row that
 * offers `Make Member` and `Delete` at full weight on the only admin is a
 * two-click path to an instance nobody can sign in to, answered with a 400
 * after the click. The guard moves that answer onto the row.
 */
export interface UserRowGuard {
	/** The target is an admin and the only one left. */
	lastAdmin: boolean;
	/** The target is the account the operator is signed in as. */
	isSelf: boolean;
	/** Shown next to the row. A disabled button is not focusable, so the
	 *  reason has to be somewhere a keyboard user can read. */
	reason: string;
}

export function guardUserRow(
	target: ManagedUser,
	users: ManagedUser[],
	currentUsername: string | null
): UserRowGuard {
	const lastAdmin = target.is_admin && users.filter((u) => u.is_admin).length <= 1;
	const isSelf = currentUsername !== null && target.username === currentUsername;

	let reason = '';
	if (isSelf && lastAdmin) {
		reason = 'The account you are signed in as, and the only admin.';
	} else if (isSelf) {
		reason = 'The account you are signed in as.';
	} else if (lastAdmin) {
		reason = 'The only admin here. Promote another admin first.';
	}

	return { lastAdmin, isSelf, reason };
}

/**
 * Whether `Make Member` may be offered for this target. This is exactly what the
 * API enforces and no more: demoting yourself while another admin is left is
 * recoverable by that other admin, so the UI does not forbid a thing the server
 * allows.
 */
export function demoteBlocked(guard: UserRowGuard): boolean {
	return guard.lastAdmin;
}

/**
 * Whether `Delete` may be offered. Deleting the only admin is the same call as
 * deleting your own account wearing a different name, so the server's self-
 * delete rule already covers both.
 */
export function deleteBlocked(guard: UserRowGuard): boolean {
	return guard.lastAdmin || guard.isSelf;
}
