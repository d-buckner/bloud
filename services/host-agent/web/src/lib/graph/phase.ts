// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The lifecycle phase a node should read as.
 *
 * The backend sends two words for one node: `phase`, what the engine is seeing
 * right now, and `status`, what the store recorded. They agree in steady state
 * and disagree during a pass, and the live one is the only thing worth drawing on
 * a graph that polls every half second. A node the engine does not track (an
 * external provider, the public address) sends no phase at all, and falls back to
 * its status rather than being drawn as a node with no state.
 */
export function phaseWord(phase: string | undefined, status: string): string {
	return phase && phase.length > 0 ? phase : status;
}

/**
 * Whether the engine tracks this node at all.
 *
 * An external provider, the public address, and an app the catalog knows but has
 * never installed all send no phase: nothing reconciles them, so there is no
 * state to report. They get a hollow dot rather than a filled one, because a
 * filled dot on the public address is a claim that something checked it, and
 * nothing did.
 */
export function isUnprobed(phase: string | undefined): boolean {
	return !phase;
}

/** A phase that means the node stopped on a problem rather than progressing. */
export function isFailure(phase: string): boolean {
	return phase === 'failed' || phase === 'error';
}

/**
 * Whether the engine has this node in hand.
 *
 * An errored node is excluded: ERROR is terminal until something resets it, so
 * nothing is working on it, and pulsing it would send the reader to the one node
 * nobody is touching.
 */
export function isWorking(inFlight: boolean | undefined, phase: string): boolean {
	return inFlight === true && !isFailure(phase);
}

/** The phases that mean "done", and so are not worth calling out on a card. */
const SETTLED = new Set(['running', 'active', 'external', 'catalog']);

/**
 * Whether a card should name its phase in words.
 *
 * A box of five containers all saying "running" is five words that say nothing;
 * the green dots already say it. The moment one of them is doing something, that
 * one earns the words.
 *
 * This is the rule for every card, box, flat node and container alike, and not
 * just the containers it was written for: a box that showed "running" because it
 * happened to be wide enough while its narrower neighbours hid the word made the
 * settled state look like information about one app rather than the absence of
 * news about all of them.
 */
export function showsPhase(phase: string): boolean {
	return !SETTLED.has(phase);
}
