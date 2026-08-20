// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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
		watch: {
			// Use polling for NFS/network mounts
			usePolling: true,
			interval: 1000
		},
		fs: {
			// Allow serving files outside web project
			allow: [
				'..'  // Parent directories
			]
		}
	}
});
