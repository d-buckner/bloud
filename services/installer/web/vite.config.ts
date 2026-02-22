import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		host: '0.0.0.0',
		allowedHosts: ['bloud.local', 'localhost'],
		watch: {
			usePolling: true,
			interval: 1000
		},
		proxy: {
			'/api': 'http://localhost:3001'
		}
	}
});
