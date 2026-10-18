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
 *  - **A completion that is not the response.** A command that shows a claim is
 *    done when confirmed state carries that claim, and the overlay decides what
 *    "carries" means. The check reads the confirmed slices, never the projection,
 *    so a command cannot confirm itself with the value it just invented.
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
import { overlayReady } from './overlay';
import type { OptimisticPatch } from './overlay';
import type { CommandKey, SettleKind } from './reducers/commands';

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
	/**
	 * A read that refreshes the confirmed slice. With a claim this marks a
	 * convergence command, which polls until the claim is true. Without one it is
	 * a reread: one read, then done.
	 */
	read?: () => Promise<unknown>;
	/** How the wait spends its time. Only meaningful with a claim and `read`. */
	window?: PollWindow;
	/** The toast a rejected write raises. No string means the caller shows it. */
	failure?: string;
	/** The toast the cap raises when the write was accepted and never applied. */
	unconfirmed?: string;
}

/**
 * Which settle kind a spec describes, for the record and for a reader of the
 * log. A claim plus a read converges, a read alone re-reads, and no read is the
 * response itself.
 */
function kindOf<T>(spec: CommandSpec<T>): SettleKind {
	if (!spec.read) return 'request';
	return spec.optimistic || spec.refine ? 'convergence' : 'reread';
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

		// The claim is the settle condition as well as what the user sees. It is
		// refined once, here, so a command cannot wait for a different value than
		// the one it showed.
		let claim = spec.optimistic ?? null;
		if (spec.refine) {
			claim = spec.refine(value);
			dispatch(commandRefined({ key: spec.key, patch: claim }));
		}

		const outcome = await settle(spec, claim, isCancelled);
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
 * Wait for the claim the command showed.
 *
 * The response is the whole answer when there is no read. A read without a claim
 * is a reread: the response stored something, and the read that shows it ends
 * the command. A read with a claim is a convergence command, and it is done when
 * confirmed state carries the claim.
 */
function settle<T>(
	spec: CommandSpec<T>,
	claim: OptimisticPatch | null,
	isCancelled: () => boolean
): Promise<'settled' | 'unconfirmed' | 'superseded'> {
	if (!spec.read) return Promise.resolve(isCancelled() ? 'superseded' : 'settled');
	if (!claim) {
		return spec.read().then(() => (isCancelled() ? 'superseded' : 'settled'));
	}
	return awaitConvergence(claim, spec.read, spec.window, isCancelled);
}

function reportFailure<T>(spec: CommandSpec<T>, error: string, toast: string | undefined): void {
	dispatch(commandFailed({ key: spec.key, error, at: Date.now() }));
	if (toast) dispatch(toastPushed({ tone: 'error', message: toast }));
}

type ConvergenceOutcome = 'settled' | 'unconfirmed' | 'superseded';

/**
 * Wait for reality to agree.
 *
 * The condition is the claim itself: `overlayReady` asks the overlay whether
 * confirmed state carries it. That is the same rule the selector uses to decide
 * whether to keep showing the claim, so the two cannot drift.
 *
 * The loop reads only to refresh the confirmed slice. A failed read is not a
 * failed save: the wait has its own deadline, and the next read is the retry.
 * That is why a network blip during a ten minute wait cannot turn a healthy save
 * into a toast.
 */
async function awaitConvergence(
	claim: OptimisticPatch,
	read: () => Promise<unknown>,
	window: PollWindow | undefined,
	isCancelled: () => boolean
): Promise<ConvergenceOutcome> {
	const budget = window ?? SAVE_WINDOW;
	const deadline = Date.now() + budget.capMs;
	let wait = budget.firstMs;
	let ready = overlayReady(claim, getState());

	while (!ready && Date.now() < deadline) {
		if (isCancelled()) return 'superseded';
		await delay(wait);
		wait = Math.min(wait * 2, budget.maxMs);
		if (isCancelled()) return 'superseded';
		await read().catch(() => undefined);
		ready = overlayReady(claim, getState());
	}

	if (ready) return 'settled';
	return isCancelled() ? 'superseded' : 'unconfirmed';
}

export function messageOf(err: unknown): string {
	if (err instanceof Error) return err.message;
	if (typeof err === 'object' && err !== null && 'message' in err) {
		return String((err as { message: unknown }).message);
	}
	return String(err);
}
