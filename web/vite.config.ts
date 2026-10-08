import { sveltekit } from '@sveltejs/kit/vite';
import adapter from '@sveltejs/adapter-static';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// `pnpm dev` proxies the API to a manager started with `task dev:api`.
const api = process.env.RSM_API ?? 'http://localhost:40125';

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: { runes: true },
			// A single-page app: the Go binary serves index.html for every panel route.
			adapter: adapter({ pages: 'dist', assets: 'dist', fallback: 'index.html', strict: false })
		})
	],
	server: {
		proxy: {
			'/api': { target: api, ws: true, changeOrigin: false }
		}
	}
});
