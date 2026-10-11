// SPDX-License-Identifier: AGPL-3.0-only
/**
 * What each edge of the developer graph means, and how much of that it draws.
 *
 * Every edge arrives from the backend as a bare label, and the label is the only
 * thing that says what kind of wiring it is: `proxy` is Traefik routing to an
 * app, `native-oidc` is an identity provider, `mcp` or `pvr` is an integration
 * contract, and an empty label is a within-app `dependsOn` that the app's own box
 * already scopes.
 *
 * The classification is kept, and the drawing is not. There was a version of this
 * file with a stroke per kind, and the graph it produced was a swatch test: a
 * dotted purple line, a dashed blue line, a marching black line and a thin gray
 * one, where the arrow and the label already said everything the colors did. A
 * relationship you can only see by its hue is also one you cannot see at all
 * without that hue. So one line draws everything, and the kind survives as the
 * decision of which labels are worth the pixels.
 */

export type EdgeKind = 'ingress' | 'proxy' | 'identity' | 'integration' | 'depends';

/**
 * How every edge draws, for every kind.
 *
 * One pixel: the graph has ~15 edges and the arrows are short, so anything
 * heavier than the container borders starts to read as the subject of the
 * picture rather than the wiring under it.
 *
 * `#57534E` on the canvas (#FAF9F6) is 7.0:1, far past the 3:1 a meaningful
 * graphic needs (WCAG 1.4.11), so a line this thin is still a line you can see.
 */
export const EDGE_STYLE = { stroke: '#57534E', width: 1 } as const;

/**
 * Whether an edge of this kind draws its label.
 *
 * `proxy` names a relationship the arrow already says, and four boxes all
 * labelled "proxy" is four words that identify nothing. The contract names are
 * the ones a reader has to be able to read off the line.
 */
const SHOWS_LABEL: Record<EdgeKind, boolean> = {
	ingress: true,
	proxy: false,
	identity: true,
	integration: true,
	depends: false
};

/** The SSO strategies, which are the identity edges (see invariant 6). */
const IDENTITY_LABELS = new Set(['native-oidc', 'ldap', 'forward-auth', 'sso']);

/** Classify one edge from its backend label. */
export function edgeKind(label: string): EdgeKind {
	if (!label) return 'depends';
	if (label === 'route') return 'ingress';
	if (label === 'proxy') return 'proxy';
	if (IDENTITY_LABELS.has(label)) return 'identity';
	return 'integration';
}

/** Whether an edge of this kind carries its label onto the canvas. */
export function showsLabel(kind: EdgeKind): boolean {
	return SHOWS_LABEL[kind];
}
