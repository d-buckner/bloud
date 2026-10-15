// SPDX-License-Identifier: AGPL-3.0-only
import type { CatalogApp } from '$lib/types';
import { formatSizeMB } from '$lib/utils/format';

/**
 * What an install does, said before the person presses the button.
 *
 * The catalog card carries a name, a size, and one line of description. That is
 * not enough to decide to install a multi-gigabyte app: the same decision that
 * asks about disk space also creates an identity provider client, wires
 * credentials between containers, and can pull a second app with it. Bloud holds
 * that knowledge in the app's metadata, so the modal can state it instead of
 * leaving the person to discover it after the fact.
 *
 * Everything here is read from the catalog entry. Nothing is estimated: an app
 * that declares no size gets no size line, and an app that declares no
 * integrations gets no wiring section. A brief that guesses is worse than a
 * brief that is short.
 */

/** What the brief needs to know about this instance and catalog. */
export interface BriefContext {
	/** True when the named catalog app is installed on this instance. */
	isInstalled(name: string): boolean;
	/**
	 * True when the named catalog app is one a person can install. System apps
	 * (Traefik, Authentik) are always there and never worth naming: "connects to
	 * the reverse proxy" describes nothing the person chose.
	 */
	isUserApp(name: string): boolean;
	/** The display name of a catalog id, or the id itself when unknown. */
	displayName(name: string): string;
}

/** One titled block of the brief. An empty block is dropped, not rendered. */
export interface BriefSection {
	title: string;
	lines: string[];
}

/**
 * The role a contract plays, in the words a person scanning the modal needs.
 * The metadata key is a developer identifier (`mediaServer`, `pvr`); this table
 * is the only place it is translated. A contract that is not listed falls back
 * to its own name, so a new contract still reads as something.
 */
const CONTRACT_ROLES: Record<string, string> = {
	agentApi: 'agent API',
	appApi: 'API access',
	caldav: 'calendar and contacts',
	downloadClient: 'download client',
	icsFeed: 'calendar feed',
	inference: 'model endpoint',
	mcp: 'MCP tools',
	mediaServer: 'media server',
	proxy: 'reverse proxy',
	pvr: 'media library manager',
	requestManager: 'media request manager',
	sso: 'single sign-on',
	transcription: 'speech to text'
};

/** The sign-in sentence for each SSO strategy. See invariant 6 in AGENTS.md. */
const SIGN_IN_COPY: Record<string, string> = {
	'native-oidc': '{app} signs in with your Bloud account. Bloud registers it with the identity provider.',
	ldap: '{app} signs in against the Bloud directory. You use your Bloud account.',
	'forward-auth': 'Bloud checks your Bloud session before {app} opens. You use your Bloud account.',
	none: '{app} keeps its own account and password. Bloud does not sign you in to it.'
};

function role(contract: string): string {
	return CONTRACT_ROLES[contract] ?? contract;
}

function joinNames(names: string[]): string {
	if (names.length <= 1) return names[0] ?? '';
	if (names.length === 2) return `${names[0]} and ${names[1]}`;
	return `${names.slice(0, -1).join(', ')}, and ${names[names.length - 1]}`;
}

/** The sign-in block: what happens to the person's credentials. */
function signInSection(app: CatalogApp, displayName: string): BriefSection {
	const strategy = app.sso?.strategy ?? 'none';
	const template = SIGN_IN_COPY[strategy] ?? SIGN_IN_COPY.none;
	const lines = [template.replace('{app}', displayName)];
	if (strategy !== 'none' && app.sso?.userCreation) {
		lines.push(`Bloud creates a ${displayName} account for each Bloud user at first sign-in.`);
	}
	return { title: 'Sign-in', lines };
}

/**
 * The providers one contract can be satisfied by, with the default first.
 *
 * The default is what `required: true` installs alongside the app, so the order
 * is the order the resolver uses and not a preference of this file. System
 * providers are dropped by the caller: nobody chose the reverse proxy, so a
 * line about it describes nothing the person decided.
 */
function providersFor(compatible: { app?: string; default?: boolean }[]): string[] {
	const named = compatible.map((c) => c.app).filter((n): n is string => Boolean(n));
	return [...named].sort((a, b) => {
		const rank = (name: string) => (compatible.find((c) => c.app === name)?.default ? 0 : 1);
		return rank(a) - rank(b);
	});
}

/** The wiring block: which other apps this one talks to, and when. */
function wiringSection(app: CatalogApp, ctx: BriefContext): BriefSection {
	const lines: string[] = [];
	for (const [contract, decl] of Object.entries(app.integrations ?? {})) {
		const providers = providersFor(decl.compatible ?? []).filter((p) => ctx.isUserApp(p));
		if (providers.length === 0) continue;
		const here = providers.filter((p) => ctx.isInstalled(p)).map(ctx.displayName);
		if (here.length > 0) {
			lines.push(`Connects to ${joinNames(here)} (${role(contract)}).`);
			continue;
		}
		if (decl.required) {
			const [first] = providers;
			if (first) {
				lines.push(`Installs ${ctx.displayName(first)} with it, then connects (${role(contract)}).`);
				continue;
			}
		}
		lines.push(`Works with ${joinNames(providers.map(ctx.displayName))} (${role(contract)}) if you install one.`);
	}
	return { title: 'Connections', lines };
}

/** The download block: what this costs the machine. */
function machineSection(app: CatalogApp, ctx: BriefContext): BriefSection {
	const lines: string[] = [];
	const size = formatSizeMB(app.estimatedSizeMB);
	// The label already carries the tilde, so the sentence does not say "about" as
	// well. Two hedges on one number read as a guess.
	if (size) lines.push(`${size} to download.`);
	const containers = app.containers?.length ?? 0;
	if (containers > 1) lines.push(`Runs ${containers} containers.`);
	const alongside = requiredProviders(app, ctx).map(ctx.displayName);
	if (alongside.length > 0) lines.push(`Also installs ${joinNames(alongside)}.`);
	return { title: 'On this machine', lines };
}

/** The user-facing providers a `required: true` contract pulls in when absent. */
function requiredProviders(app: CatalogApp, ctx: BriefContext): string[] {
	const out: string[] = [];
	for (const decl of Object.values(app.integrations ?? {})) {
		if (!decl.required) continue;
		const [first] = providersFor(decl.compatible ?? []).filter(
			(name) => ctx.isUserApp(name) && !ctx.isInstalled(name)
		);
		if (first) out.push(first);
	}
	return out;
}

/**
 * The brief for one catalog entry, in display order. Sections with nothing to
 * say are left out, so an app with no integrations does not get an empty
 * "Connections" heading.
 */
export function installBrief(app: CatalogApp, ctx: BriefContext): BriefSection[] {
	const displayName = app.displayName || app.catalogId;
	return [
		signInSection(app, displayName),
		wiringSection(app, ctx),
		machineSection(app, ctx)
	].filter((section) => section.lines.length > 0);
}
