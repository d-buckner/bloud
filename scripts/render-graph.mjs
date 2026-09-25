#!/usr/bin/env node
// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Render the Bloud catalog graph to a PNG, in a real browser.
 *
 * The picture the README embeds is not drawn by a diagramming tool: it is the
 * dashboard's own developer graph, rendered by the same Svelte components and
 * the same dagre layout, from a catalog snapshot that `bloud depgraph --json`
 * derives from every app's metadata.yaml. That is the whole point: the README
 * image and the dashboard cannot drift, because they are the same code with a
 * different input.
 *
 * What it does:
 *   1. serves the built static frontend from a loopback port
 *   2. injects the catalog snapshot into the page (no snapshot file is ever
 *      written into the served tree)
 *   3. opens /graph, waits for the renderer's ready flag
 *   4. screenshots the viewport to the output PNG
 *
 * Usage:
 *   node scripts/render-graph.mjs --graph catalog.json --output docs/assets/dependency-graph.png
 *
 * Flags:
 *   --graph <file>   catalog snapshot JSON (required; "-" reads stdin)
 *   --output <file>   PNG to write (required)
 *   --build <dir>     static build to serve
 *                     (default: services/host-agent/web/build)
 *   --width <px>     downscale the picture to this device width; 0 (default)
 *                    renders at the graph's natural size
 *   --scale <n>      device scale factor (default 3: the text stays sharp when
 *                    the picture is opened full size and zoomed)
 *   --max-zoom <n>   never scale the graph past this (default 1: natural size)
 *   --timeout <ms>   render wait budget (default 60000)
 */
import { createServer } from 'node:http';
import { createReadStream } from 'node:fs';
import { access, mkdir, readFile, stat } from 'node:fs/promises';
import { dirname, extname, join, normalize, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { chromium } from 'playwright';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');

const CONTENT_TYPES = {
	'.html': 'text/html; charset=utf-8',
	'.js': 'text/javascript; charset=utf-8',
	'.mjs': 'text/javascript; charset=utf-8',
	'.css': 'text/css; charset=utf-8',
	'.json': 'application/json; charset=utf-8',
	'.svg': 'image/svg+xml',
	'.png': 'image/png',
	'.jpg': 'image/jpeg',
	'.ico': 'image/x-icon',
	'.woff': 'font/woff',
	'.woff2': 'font/woff2',
	'.ttf': 'font/ttf',
	'.map': 'application/json; charset=utf-8'
};

function usageError(message) {
	console.error(`render-graph: ${message}`);
	console.error(
		'usage: node scripts/render-graph.mjs --graph <file> --output <file> ' +
			'[--build <dir>] [--width <px>] [--scale <n>] [--max-zoom <n>] [--timeout <ms>]'
	);
	process.exit(1);
}

/** Parse argv into the render options, with the documented defaults. */
function parseArgs(argv) {
	const opts = {
		graph: '',
		output: '',
		build: join(REPO, 'services/host-agent/web/build'),
		width: 0,
		scale: 3,
		maxZoom: 1,
		timeout: 60_000
	};

	for (let i = 0; i < argv.length; i += 1) {
		const flag = argv[i];
		const value = argv[i + 1];
		if (
			!['--graph', '--output', '--build', '--width', '--scale', '--max-zoom', '--timeout'].includes(flag)
		) {
			usageError(`unknown flag ${flag}`);
		}
		if (!value) usageError(`${flag} needs a value`);
		i += 1;
		switch (flag) {
			case '--graph':
				opts.graph = value;
				break;
			case '--output':
				opts.output = value;
				break;
			case '--build':
				opts.build = resolve(REPO, value);
				break;
			case '--width':
				opts.width = positiveInt(flag, value);
				break;
			case '--scale':
				opts.scale = positiveNumber(flag, value);
				break;
			case '--max-zoom':
				opts.maxZoom = positiveNumber(flag, value);
				break;
			case '--timeout':
				opts.timeout = positiveInt(flag, value);
				break;
			default:
				break;
		}
	}

	if (!opts.graph) usageError('--graph is required ("-" reads stdin)');
	if (!opts.output) usageError('--output is required');
	return opts;
}

function positiveInt(flag, value) {
	const parsed = Number.parseInt(value, 10);
	if (!Number.isFinite(parsed) || parsed <= 0) usageError(`${flag} wants a positive integer, got ${value}`);
	return parsed;
}

function positiveNumber(flag, value) {
	const parsed = Number.parseFloat(value);
	if (!Number.isFinite(parsed) || parsed <= 0) usageError(`${flag} wants a positive number, got ${value}`);
	return parsed;
}

/** Read the catalog snapshot from a file, or from stdin when it is "-". */
async function readSnapshot(spec) {
	if (spec === '-') return readFile(0, 'utf8');
	try {
		return await readFile(spec, 'utf8');
	} catch (err) {
		usageError(`cannot read the catalog snapshot (${err.message})`);
	}
}

/** Resolve a request path to a file inside the build dir, or null if it escapes. */
function resolveStatic(buildDir, urlPath) {
	const decoded = decodeURIComponent(urlPath.split('?')[0].split('#')[0]);
	const target = normalize(join(buildDir, decoded));
	if (target !== buildDir && !target.startsWith(buildDir + '/')) return null;
	return target;
}

/**
 * Serve the static build with an SPA fallback: anything that is not a file on
 * disk gets index.html, which is how host-agent serves the bundle too, so the
 * client router resolves /graph the same way it does in production.
 */
function startServer(buildDir) {
	return new Promise((start, fail) => {
		const server = createServer(async (req, res) => {
			const target = resolveStatic(buildDir, req.url ?? '/');
			const file = target && (await isFile(target)) ? target : join(buildDir, 'index.html');
			if (!(await isFile(file))) {
				res.writeHead(404, { 'content-type': 'text/plain' });
				res.end('not found');
				return;
			}
			res.writeHead(200, {
				'content-type': CONTENT_TYPES[extname(file)] ?? 'application/octet-stream',
				'cache-control': 'no-store'
			});
			createReadStream(file).pipe(res);
		});
		server.on('error', fail);
		server.listen(0, '127.0.0.1', () => start(server));
	});
}

async function isFile(path) {
	try {
		return (await stat(path)).isFile();
	} catch {
		return false;
	}
}

/** CSS px of breathing room between the graph and the edge of the picture. */
const PAD = 24;

/**
 * Fonts the picture is drawn in. If one of these is missing the browser
 * substitutes something else and every label changes shape, which is a broken
 * render rather than a stylistic choice. Only the self-hosted families are
 * asserted: the graph draws its app names in Newsreader and its container
 * chips in the mono stack, and Inter is not part of either.
 */
const REQUIRED_FONTS = ['Newsreader'];

/**
 * Union box of everything drawn in the flow, in graph units. Call it with the
 * viewport at zoom 1 so the numbers are the graph's own, not a scaled copy.
 * Edges and their labels are included: a wide `native-oidc` label sticks out
 * past the nodes it joins, and cropping on the nodes alone would cut it.
 */
async function measureGraph(page) {
	return page.evaluate(() => {
		const selector = '.svelte-flow__node, .svelte-flow__edge, .svelte-flow__edge-label';
		let minX = Infinity;
		let minY = Infinity;
		let maxX = -Infinity;
		let maxY = -Infinity;
		let seen = 0;
		for (const el of document.querySelectorAll(selector)) {
			const rect = el.getBoundingClientRect();
			if (rect.width === 0 || rect.height === 0) continue;
			seen += 1;
			minX = Math.min(minX, rect.left);
			minY = Math.min(minY, rect.top);
			maxX = Math.max(maxX, rect.right);
			maxY = Math.max(maxY, rect.bottom);
		}
		if (seen === 0) throw new Error('nothing drawn in the flow to measure');
		return { x: minX, y: minY, width: maxX - minX, height: maxY - minY };
	});
}

async function main() {
	const opts = parseArgs(process.argv.slice(2));
	const snapshot = await readSnapshot(opts.graph);

	// Fail before launching a browser if the snapshot is not the graph shape.
	let parsed;
	try {
		parsed = JSON.parse(snapshot);
	} catch (err) {
		usageError(`the catalog snapshot is not valid JSON (${err.message})`);
	}
	if (!Array.isArray(parsed.nodes) || parsed.nodes.length === 0) {
		usageError('the catalog snapshot has no nodes');
	}

	try {
		await access(join(opts.build, 'index.html'));
	} catch {
		usageError(`no built frontend at ${opts.build} (run: npm run build --workspace=@bloud/host-agent-web)`);
	}

	const server = await startServer(opts.build);
	const port = server.address().port;
	const browser = await chromium.launch();
	const consoleErrors = [];

	/** Open the graph at a fixed zoom with fit disabled, and wait for the draw. */
	const openGraph = async (target, zoom) => {
		await target.goto(`http://127.0.0.1:${port}/graph?fit=0&zoom=${zoom}`, { waitUntil: 'load' });
		await target.waitForSelector('body[data-graph-ready="true"]', { timeout: opts.timeout });
		const renderError = await target.evaluate(() => document.body.dataset.graphError || '');
		if (renderError) throw new Error(`the graph page failed to render: ${renderError}`);
	};

	const newPage = async (viewport) => {
		const target = await browser.newPage({
			viewport,
			deviceScaleFactor: opts.scale,
			// The picture is static; a reduced-motion render is what we want,
			// not whatever the host's media settings happen to be.
			reducedMotion: 'reduce'
		});
		// The page reads the snapshot off window, so nothing has to be served
		// from it and no generated file lands in the tree.
		await target.addInitScript(`window.__BLOUD_CATALOG_GRAPH__ = ${JSON.stringify(parsed)};`);
		target.on('console', (msg) => {
			if (msg.type() === 'error') consoleErrors.push(msg.text());
		});
		target.on('pageerror', (err) => consoleErrors.push(String(err)));
		return target;
	};

	/**
	 * The picture is only the picture if the type is the dashboard's type. A
	 * font that failed to load swaps in a fallback and quietly changes how every
	 * box reads, so fail rather than ship a picture drawn in the wrong face.
	 */
	const assertFonts = async (target) => {
		const fonts = await target.evaluate(async () => {
			await document.fonts.ready;
			const loaded = new Set();
			document.fonts.forEach((f) => {
				if (f.status === 'loaded') loaded.add(f.family.replace(/["']/g, ''));
			});
			return { status: document.fonts.status, loaded: [...loaded] };
		});
		for (const family of REQUIRED_FONTS) {
			const usable = await target.evaluate((f) => document.fonts.check(`600 13px "${f}"`), family);
			if (!fonts.loaded.includes(family) || !usable) {
				throw new Error(
					`font "${family}" did not load (status ${fonts.status}, loaded: ${fonts.loaded.join(', ') || 'none'})`
				);
			}
		}
	};

	try {
		// Pass one: measure. The graph opens at zoom 1 with no fit, so the box
		// it draws is the graph's own size, not a fitted copy.
		let shot = await newPage({ width: 1600, height: 900 });
		await openGraph(shot, 1);
		const natural = await measureGraph(shot);
		const nodeCount = await shot.locator('.svelte-flow__node').count();
		if (nodeCount === 0) throw new Error('the graph rendered no nodes');
		await assertFonts(shot);

		// Natural size by default: drawing the graph smaller than it is means
		// rasterizing the text at a fraction of its design size, and no amount
		// of device scale factor puts the crispness back.
		let zoom = opts.maxZoom;
		if (opts.width > 0) {
			zoom = Math.min(opts.maxZoom, (opts.width / opts.scale - PAD * 2) / natural.width);
		}
		if (!(zoom > 0)) throw new Error(`computed a useless zoom (${zoom}) for a ${natural.width}px graph`);

		const frame = {
			width: Math.round(natural.width * zoom + PAD * 2),
			height: Math.round(natural.height * zoom + PAD * 2)
		};

		// A zoom change after the first paint makes Chromium scale the bitmap it
		// already rasterized, which is how a 2x capture comes out pixelated. At
		// natural size the pan is the only change and a pan does not resample;
		// anything else has to be a fresh page that opens at that zoom.
		if (Math.abs(zoom - 1) > 1e-6) {
			await shot.close();
			shot = await newPage(frame);
			await openGraph(shot, zoom);
		} else {
			await shot.setViewportSize(frame);
		}

		await shot.evaluate(
			({ x, y, zoom }) => window.__BLOUD_GRAPH_VIEW__.setViewport({ x, y, zoom }),
			{ x: PAD - natural.x * zoom, y: PAD - natural.y * zoom, zoom }
		);
		await shot.waitForTimeout(250);

		await mkdir(dirname(resolve(opts.output)), { recursive: true });
		await shot.screenshot({ path: opts.output });

		console.log(
			`rendered ${nodeCount} nodes to ${opts.output} ` +
				`(${frame.width}x${frame.height} css @${opts.scale}x, zoom ${zoom.toFixed(3)})`
		);
		if (consoleErrors.length > 0) {
			console.error(`the page logged ${consoleErrors.length} console error(s):`);
			for (const line of consoleErrors.slice(0, 5)) console.error(`  ${line}`);
			process.exitCode = 1;
		}
	} finally {
		await browser.close();
		server.close();
	}
}

await main();
