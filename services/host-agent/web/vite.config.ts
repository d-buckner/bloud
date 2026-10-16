// SPDX-License-Identifier: AGPL-3.0-only
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	test: {
		exclude: ['e2e/**', 'node_modules/**']
	},
	ssr: {
		// Fix Vite 6 SSR circular dependency with Svelte 5 stores
		// gridstack is in dependencies but must be bundled (adapter-static has no runtime)
		noExternal: ['svelte', 'gridstack']
	},
	server: {
		host: '0.0.0.0',
		allowedHosts: ['bloud.local', 'localhost'],
		hmr: {
			// The browser loads the dashboard from the host-agent origin, so a
			// socket on the default path rides the same proxy. The dev loop
			// restarts host-agent on every backend change, and a dropped HMR
			// socket makes the vite client reload the page. That throws away the
			// store the kernel keeps alive across a hot swap.
			//
			// So the socket gets a path of its own, and `./bloud dev` writes a
			// Traefik route that sends this path straight to vite (see
			// cli/dev_vite_route.go). Traefik and vite both outlive a host-agent
			// restart, so the socket survives one and the page keeps its state.
			// The browser still only needs the one public port. The route names
			// vite's own port (5173, the same one `vite dev --port` binds), and
			// TestDevViteRouteAgreesWithTheViteConfig fails the build if the two
			// drift apart.
			path: 'bloud-hmr'
		},
		watch: {
			// The static adapter output is never part of the dev graph. Watching
			// it costs a poll over every built file, and a `vite build` or
			// `./bloud validate` mid-session makes its changed index.html look
			// like a page change.
			ignored: ['**/build/**'],
			// Polling costs about half a second per edit, so it is opt-in. Set
			// BLOUD_DEV_VITE_POLLING=1 on a filesystem where inotify does not
			// report writes, for example a mounted directory in a VM.
			usePolling: process.env.BLOUD_DEV_VITE_POLLING === '1'
		},
		fs: {
			// Allow serving files outside web project
			allow: [
				'..'  // Parent directories
			]
		}
	}
});
