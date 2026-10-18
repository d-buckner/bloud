// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The command engine - one way to change the server, and one way to wait for it.
 *
 * This module is private to `store/commands/`. A component calls a named
 * operation (`commands.savePublicURL`), never this. The difference is the whole
 * design: the operation knows its key, its settle kind, its overlay, its poll
 * window, and the words its failure shows, and a call site cannot get one of
 * those wrong because it never states one.
 *
 * A command is not a fetch helper. Three things it owns that a bare `fetch` did
 * not:
 *
 *  - **Dedupe.** A second call on a pending key either joins the first command or
 *    supersedes it, and the operation decides which. Join is right for install:
 *    a double click is one install. Supersede is right for a save: the newest
 *    value must win, and silently dropping it is the bug this replaces.
 *  - **The optimistic overlay.** A command declares the value to show. It lives
 *    in the command record, so it exists exactly while the command is pending,
 *    and a terminal status drops it.
 *  - **A completion that is not the response.** `settled` is a predicate over
 *    confirmed state. It is checked against `state`, never against a projection
 *    of the overlay, so a command cannot confirm itself with the value it just
 *    invented. That rule is why `settings` holds confirmed values only.
 *
 * Nothing here throws. The outcome is the return value, and the store holds the
 * record either way.
 */

import { dispatch, getState } from './kernel';
import {
	commandFailed,
	commandRefined,
	commandSettled,
	commandStarted,
	commandSuperseded,
	toastPushed
} from './actions';
import type { OptimisticPatch } from './overlay';
import type { CommandKey, SettleKind } from './reducers/commands';
import type { RootState } from './reducers';

export type { CommandKey, SettleKind };

export type CommandResult<T> =
	| { ok: true; value: T }
	| { ok: false; error: string; superseded?: true };

/** How a convergence wait spends its time. */
export interface PollWindow {
	/** The first pause. Most saves have landed by the time a response is parsed. */
	firstMs: number;
	/** The ceiling the pause doubles up to, so a slow apply is not hammered. */
	maxMs: number;
	/** When to stop waiting and report the write as not applied. */
	capMs: number;
}

/**
 * The one window every settings save shares.
 *
 * A timezone save recreates every container on the instance, so minutes is a
 * normal wait and the cap has to cover it. The backoff matters for the same
 * reason: 500ms is right while the answer is likely seconds away, and 5s is
 * right once it is minutes away.
 */
export const SAVE_WINDOW: PollWindow = { firstMs: 500, maxMs: 5_000, capMs: 10 * 60_000 };

export interface CommandSpec<T> {
	/** The dedupe key and the store key: same thing. */
	key: CommandKey;
	/**
	 * What a second call with this key does while the first is pending. Default
	 * `join`: a repeated identical request is one operation.
	 */
	repeat?: 'join' | 'replace';
	/** The request. Rejecting is a failure; a 202 on an intent route is not a settle. */
	run: () => Promise<T>;
	/** The value to show while the command is pending. */
	optimistic?: OptimisticPatch;
	/**
	 * Replace the overlay with what the server stored. The address parser fills in
	 * a scheme and the time zone save trims, so the value that was typed is not
	 * always the value that will confirm. Without this the overlay can never
	 * match, and the poll runs to its cap over a save that already landed.
	 */
	refine?: (result: T) => OptimisticPatch | null;
	/** A read that refreshes the confirmed slice `settled` looks at. */
	read?: () => Promise<unknown>;
	/** Has reality caught up? A predicate over confirmed state, never over a view. */
	settled?: (confirmed: RootState, result: T) => boolean;
	/** Only meaningful with `settled`. */
	window?: PollWindow;
	/** The toast a rejected write raises. No string means the caller shows it. */
	failure?: string;
	/** The toast the cap raises when the write was accepted and never applied. */
	unconfirmed?: string;
}

/** Which settle kind a spec describes. Derived, so a spec cannot contradict itself. */
function kindOf<T>(spec: CommandSpec<T>): SettleKind {
	if (spec.settled) return 'convergence';
	if (spec.read) return 'reread';
	return 'request';
}

interface Execution {
	promise: Promise<CommandResult<unknown>>;
	/** Signal that a newer command took this key. Dispatches the record at once. */
	supersede: () => void;
}

/** In-flight executions, so a second call joins or replaces instead of repeating. */
const inFlight = new Map<CommandKey, Execution>();

/** Is anything in flight for this key right now? */
export function isRunning(key: CommandKey): boolean {
	return inFlight.has(key);
}

function delay(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Run a command. Never throws: the outcome is the return value, and the store
 * holds the record either way.
 */
export function runCommand<T>(spec: CommandSpec<T>): Promise<CommandResult<T>> {
	const existing = inFlight.get(spec.key);
	if (existing) {
		if ((spec.repeat ?? 'join') === 'join') {
			return existing.promise as Promise<CommandResult<T>>;
		}
		// Dispatch the superseded record before the new command starts, so the new
		// pending record is the one left in the store.
		existing.supersede();
	}

	let cancelled = false;
	const supersede = (): void => {
		if (cancelled) return;
		cancelled = true;
		dispatch(commandSuperseded({ key: spec.key, at: Date.now() }));
	};

	const execution: Execution = {
		promise: execute(spec, () => cancelled) as Promise<CommandResult<unknown>>,
		supersede
	};
	inFlight.set(spec.key, execution);
	void execution.promise.finally(() => {
		// Only the newest execution owns the slot. An older one finishing late must
		// not delete the entry the newer one put there.
		if (inFlight.get(spec.key) === execution) inFlight.delete(spec.key);
	});
	return execution.promise as Promise<CommandResult<T>>;
}

const SUPERSEDED: { ok: false; error: string; superseded: true } = {
	ok: false,
	error: 'superseded',
	superseded: true
};

async function execute<T>(spec: CommandSpec<T>, isCancelled: () => boolean): Promise<CommandResult<T>> {
	dispatch(
		commandStarted({
			key: spec.key,
			kind: kindOf(spec),
			at: Date.now(),
			optimistic: spec.optimistic
		})
	);

	try {
		const value = await spec.run();
		if (isCancelled()) return SUPERSEDED;

		if (spec.refine) dispatch(commandRefined({ key: spec.key, patch: spec.refine(value) }));

		const outcome = await settle(spec, value, isCancelled);
		if (outcome === 'superseded') return SUPERSEDED;
		if (outcome === 'unconfirmed') {
			reportFailure(spec, `${spec.key} was accepted but the server never applied it`, spec.unconfirmed);
			return { ok: false, error: `${spec.key} was accepted but never applied` };
		}

		dispatch(commandSettled({ key: spec.key, at: Date.now() }));
		return { ok: true, value };
	} catch (err) {
		if (isCancelled()) return SUPERSEDED;
		const message = messageOf(err);
		dispatch(commandFailed({ key: spec.key, error: message, at: Date.now() }));
		if (spec.failure) dispatch(toastPushed({ tone: 'error', message: spec.failure }));
		return { ok: false, error: message };
	}
}

/**
 * Wait for the settle condition the spec declared.
 *
 * The response is enough for a `request` command, the follow-up read ends a
 * `reread`, and a `convergence` command polls until confirmed state agrees.
 */
function settle<T>(
	spec: CommandSpec<T>,
	result: T,
	isCancelled: () => boolean
): Promise<'settled' | 'unconfirmed' | 'superseded'> {
	if (spec.settled) return awaitConvergence(spec, result, isCancelled);
	if (!spec.read) return Promise.resolve(isCancelled() ? 'superseded' : 'settled');
	return spec.read().then(() => (isCancelled() ? 'superseded' : 'settled'));
}

function reportFailure<T>(spec: CommandSpec<T>, error: string, toast: string | undefined): void {
	dispatch(commandFailed({ key: spec.key, error, at: Date.now() }));
	if (toast) dispatch(toastPushed({ tone: 'error', message: toast }));
}

type ConvergenceOutcome = 'settled' | 'unconfirmed' | 'superseded';

/**
 * Wait for reality to agree.
 *
 * The loop reads only to refresh the confirmed slice, and it checks `settled`
 * against that slice. A failed read is not a failed save: the wait has its own
 * deadline, and the next read is the retry. That is why a network blip during a
 * ten minute wait cannot turn a healthy save into a toast.
 */
async function awaitConvergence<T>(
	spec: CommandSpec<T>,
	result: T,
	isCancelled: () => boolean
): Promise<ConvergenceOutcome> {
	const window = spec.window ?? SAVE_WINDOW;
	const deadline = Date.now() + window.capMs;
	let wait = window.firstMs;
	let confirmed = spec.settled!(getState(), result);

	while (!confirmed && Date.now() < deadline) {
		if (isCancelled()) return 'superseded';
		await delay(wait);
		wait = Math.min(wait * 2, window.maxMs);
		if (isCancelled()) return 'superseded';
		await spec.read!().catch(() => undefined);
		confirmed = spec.settled!(getState(), result);
	}

	if (confirmed) return 'settled';
	return isCancelled() ? 'superseded' : 'unconfirmed';
}

export function messageOf(err: unknown): string {
	if (err instanceof Error) return err.message;
	if (typeof err === 'object' && err !== null && 'message' in err) {
		return String((err as { message: unknown }).message);
	}
	return String(err);
}
