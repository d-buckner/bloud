// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

/**
 * The loading contract for #292.
 *
 * The initial load used to be a circular spinner on an empty canvas for two
 * seconds, and on a cold start four. Nothing about that state is caught by a
 * test that only looks at the settled page, and it is exactly the kind of thing
 * that comes back the next time a route grows a fetch: a loading branch, a
 * spinner, and a page that has no shape until the data lands.
 *
 * So the contract is asserted against the source, the same way the dialog
 * contract is. It checks that the bootstrapping state draws the shell, that every
 * route has a skeleton rather than a sentence, and that the blocks stay out of
 * the accessibility tree while a real status stays in it.
 */

const SRC = new URL('../../..', import.meta.url).pathname;
const read = (path: string) => readFileSync(join(SRC, path), 'utf8');

const layout = read('routes/+layout.svelte');
// The dashboard and the catalog moved into components: two routes render the
// dashboard (`/` and `/catalog`), and the catalog list is a modal. The skeleton
// branches moved with them, so the contract reads the components now.
const home = read('lib/components/HomeView.svelte');
const catalog = read('lib/components/CatalogModal.svelte');
const shell = read('lib/components/LoadingShell.svelte');

describe('the layout no longer has a bare spinner', () => {
	it('draws the shell while the session is being read', () => {
		expect(layout).toMatch(/session\.current\.status === 'bootstrapping'\}\s*<!--[\s\S]*?<LoadingShell \/>/);
	});

	it('keeps no spinner of its own', () => {
		expect(layout).not.toMatch(/class="spinner"/);
		expect(layout).not.toMatch(/\.spinner\s*\{/);
	});

	it('still gives the setup wizard and the sign-in redirect the last word', () => {
		// The skeleton must not swallow the two states the session read can answer
		// with. A route that rendered early would have read admin rights as "not an
		// admin" and bounced Settings to Home.
		expect(layout).toContain('session.current.setupRequired');
		expect(layout).toContain('{@render children()}');
	});
});

describe('LoadingShell', () => {
	it('hides the blocks and announces the wait', () => {
		// A skeleton is decoration: read out loud it is "image, image, image".
		// The status paragraph is the part a screen reader should hear.
		expect(shell).toContain('aria-hidden="true"');
		expect(shell).toMatch(/role="status"/);
		expect(shell).toMatch(/class="visually-hidden"/);
	});

	it('draws the three parts of the dashboard it claims to', () => {
		for (const part of ['rail', 'page-header', 'LoadingGrid']) {
			expect(shell, `missing ${part}`).toContain(part);
		}
	});
});

describe('every route has a skeleton, not a sentence', () => {
	it('shows the grid skeleton on the home route while it loads', () => {
		expect(home).toMatch(/loading\.current\}\s*\s*<LoadingGrid \/>|!mounted \|\| loading\.current\}\s*\s*<LoadingGrid \/>/);
	});

	it('shows the card skeleton on the catalog route while it loads', () => {
		expect(catalog).toMatch(/catalogLoading\}\s*\s*<CatalogSkeleton \/>/);
		expect(catalog).not.toContain('Loading catalog...');
	});

	it.each(['LoadingShell.svelte', 'LoadingGrid.svelte', 'CatalogSkeleton.svelte', 'Skeleton.svelte'])(
		'%s keeps its blocks out of the accessibility tree',
		(file) => {
			const source = read(`lib/components/${file}`);
			if (file === 'Skeleton.svelte') {
				// The primitive has no tree of its own; every caller wraps it in an
				// aria-hidden container. What it must not do is animate forever for a
				// reader who asked for no motion.
				expect(source).toContain('prefers-reduced-motion');
				return;
			}
			expect(source, `${file} must hide its skeleton`).toContain('aria-hidden="true"');
		}
	);
});
