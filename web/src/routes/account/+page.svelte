<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '#lib/api.js';
	import { session } from '#lib/session.svelte.js';
	import { attempt, toast } from '#lib/toast.svelte.js';
	import { steamProfile } from '#lib/format.js';
	import { steamError } from '#lib/steam.js';
	import { Busy } from '#lib/busy.svelte.js';
	import { ask } from '#lib/ask.svelte.js';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	let current = $state('');
	let next = $state('');
	let confirm = $state('');

	// The Steam link flow lands here with ?linked=1 or ?error=. Say so once, then
	// drop the query so a reload does not say it again. (Not an effect: pushing a
	// toast reads the toast list, which would re-run it endlessly.)
	onMount(() => {
		const q = page.url.searchParams;
		const err = steamError(q.get('error'));
		if (err) toast.error(err);
		if (q.get('linked')) toast.ok('Steam account linked');
		if (err || q.has('linked')) goto(page.url.pathname, { shallow: true, replace: true });
	});

	const busy = new Busy();
	async function changePassword(e: SubmitEvent) {
		e.preventDefault();
		if (next !== confirm) {
			toast.error('The new passwords do not match.');
			return;
		}
		const body = { current, next };
		if (await busy.run('password', () => attempt(() => api.post('/auth/password', body), 'Password changed'))) {
			current = next = confirm = '';
			session.refresh();
		}
	}

	async function unlink() {
		const body = 'You sign in with your password after this, and any in-game admin this panel gives you through Steam ends.';
		if (!(await ask({ title: 'Unlink your Steam account?', body, action: 'Unlink' }))) return;
		if (await busy.run('unlink', () => attempt(() => api.post('/auth/steam/unlink'), 'Steam unlinked'))) session.refresh();
	}

	guardUnsaved(() => current !== '' || next !== '' || confirm !== '');
</script>

{#if session.user}
	<div class="mx-auto max-w-2xl space-y-4 p-4 sm:space-y-6 sm:p-6">
		<div>
			<h1 class="page-title">{session.user.username}</h1>
			<p class="mt-2 text-sm text-muted">{session.user.owner ? 'Owner of this panel.' : 'Panel user.'}</p>
		</div>

		<section class="card p-4 sm:p-5">
			<h2>Steam</h2>
			{#if session.user.steamId}
				<p class="mt-1 text-sm text-muted">
					Linked to <a class="font-mono text-fg hover:underline" href={steamProfile(session.user.steamId)} target="_blank" rel="noreferrer"
						>{session.user.steamId}</a
					>. You can sign in with Steam.
				</p>
				<button class="btn mt-3" onclick={unlink} disabled={!session.user.hasPassword || busy.is()} title={session.user.hasPassword ? '' : 'Set a password first'}
					>Unlink Steam</button
				>
			{:else}
				<p class="mt-1 text-sm text-muted">Link your Steam account to sign in without a password.</p>
				{#if session.steam}
					<a class="btn mt-3" href="/api/auth/steam?mode=link" data-sveltekit-reload>Link Steam account</a>
				{:else}
					<p class="mt-2 text-sm text-warn">
						Steam sign-in needs the panel's address, so Steam knows where to send you back.
						{session.user.owner ? 'Set it on the Manager page.' : 'An owner can set it on the Manager page.'}
					</p>
				{/if}
			{/if}
		</section>

		<section class="card p-4 sm:p-5">
			<h2 class="mb-3">{session.user.hasPassword ? 'Change password' : 'Set a password'}</h2>
			<form class="space-y-3" onsubmit={changePassword}>
				{#if session.user.hasPassword}
					<div>
						<label class="label" for="cur">Current password</label>
						<input id="cur" class="input" type="password" autocomplete="current-password" bind:value={current} required />
					</div>
				{/if}
				<div class="grid gap-3 sm:grid-cols-2">
					<div>
						<label class="label" for="new">New password</label>
						<input id="new" class="input" type="password" autocomplete="new-password" bind:value={next} required minlength="10" />
					</div>
					<div>
						<label class="label" for="cf">Confirm</label>
						<input id="cf" class="input" type="password" autocomplete="new-password" bind:value={confirm} required />
					</div>
				</div>
				<button class="btn btn-primary" disabled={busy.is()}>{busy.is('password') ? 'Saving…' : 'Save password'}</button>
			</form>
		</section>
	</div>
{/if}
