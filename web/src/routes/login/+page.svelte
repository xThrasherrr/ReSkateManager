<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { afterSignIn, api, message } from '#lib/api.js';
	import { session } from '#lib/session.svelte.js';
	import { steamError } from '#lib/steam.js';
	import AuthShell from '#lib/components/AuthShell.svelte';

	let username = $state('');
	let password = $state('');
	let error = $state(steamError(page.url.searchParams.get('error')));
	let busy = $state(false);

	// Say a Steam sign-in's error once; a reload shouldn't say it again. Where
	// to go afterwards stays.
	onMount(() => {
		if (!page.url.searchParams.has('error')) return;
		const url = new URL(page.url.href);
		url.searchParams.delete('error');
		goto(url, { shallow: true, replace: true });
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			await api.post('/auth/login', { username, password });
			await session.refresh();
			goto(afterSignIn(page.url.searchParams.get('next')));
		} catch (err) {
			error = message(err);
		} finally {
			busy = false;
		}
	}
</script>

<AuthShell tagline="drop in." subtitle="Sign in to manage your servers.">
	<div class="card p-5">
		{#if session.steam}
			<a href="/api/auth/steam?mode=login" data-sveltekit-reload class="btn h-10 w-full bg-[#171a21] hover:bg-[#232a36]">
				<svg viewBox="0 0 24 24" class="size-4" fill="currentColor" aria-hidden="true"
					><path
						d="M11.98 0C5.67 0 .5 4.86 0 11.03l6.43 2.66a3.38 3.38 0 0 1 2.14-.6l2.86-4.15v-.06a4.52 4.52 0 1 1 4.52 4.52h-.1l-4.08 2.92c0 .05.01.1.01.16a3.39 3.39 0 0 1-6.72.64L.46 15.25A12 12 0 1 0 11.98 0zM7.54 18.21l-1.47-.6a2.54 2.54 0 1 0 1.4-3.47l1.52.63a1.87 1.87 0 1 1-1.45 3.44zm11.42-9.3a3.02 3.02 0 1 0-6.03 0 3.02 3.02 0 0 0 6.03 0zm-5.27-.01a2.27 2.27 0 1 1 4.53 0 2.27 2.27 0 0 1-4.53 0z"
					/></svg
				>
				Sign in with Steam
			</a>
			<div class="my-4 flex items-center gap-3 font-marker text-xs text-faint"><span class="h-0.5 flex-1 rounded bg-line"></span>or<span class="h-0.5 flex-1 rounded bg-line"></span></div>
		{/if}
		<form onsubmit={submit} class="space-y-3">
			<div>
				<label class="label" for="u">Username</label>
				<input id="u" class="input h-9" autocomplete="username" bind:value={username} required />
			</div>
			<div>
				<label class="label" for="p">Password</label>
				<input id="p" class="input h-9" type="password" autocomplete="current-password" bind:value={password} required />
			</div>
			{#if error}<p class="text-sm text-bad" role="alert">{error}</p>{/if}
			<button class="btn btn-primary h-10 w-full text-base" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
		</form>
	</div>
</AuthShell>
