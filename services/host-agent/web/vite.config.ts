import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	test: {
		exclude: ['e2e/**', 'node_modules/**']
	},
	ssr: {
		// Fix Vite 6 SSR circular dependency with Svelte 5 stores
		noExternal: ['svelte']
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
