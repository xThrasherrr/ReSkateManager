<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api, message } from '#lib/api.js';
	import { session } from '#lib/session.svelte.js';
	import AuthShell from '#lib/components/AuthShell.svelte';
	import AccessSettings from '#lib/components/AccessSettings.svelte';

	let pin = $state('');
	let filled = $state(false);
	let username = $state('');
	let password = $state('');
	let confirm = $state('');
	let error = $state('');
	let busy = $state(false);
	let step = $state<'owner' | 'access'>('owner');

	// The manager opens this page with the PIN after the #, which never reaches
	// the server. Take it, then drop it from the address bar and history.
	onMount(() => {
		const m = location.hash.match(/pin=(\d+)/);
		if (!m?.[1]) return;
		pin = m[1];
		filled = true;
		goto(location.pathname, { shallow: true, replace: true });
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		if (password !== confirm) {
			error = 'The passwords do not match.';
			return;
		}
		busy = true;
		try {
			await api.post('/setup', { pin, username, password });
			await session.refresh();
			step = 'access';
		} catch (err) {
			error = message(err);
		} finally {
			busy = false;
		}
	}

	// For pattern attributes, where Svelte would read the braces as its own.
	const usernamePattern = String.raw`[A-Za-z0-9_.\-]{3,32}`;
</script>

{#if step === 'owner'}
	<AuthShell wide tagline="set up your spot." subtitle="Create the owner account. You can link Steam and add more admins afterwards.">
		<form class="card space-y-3 p-5" onsubmit={submit}>
			<div>
				<label class="label" for="pin">Setup PIN</label>
				<input id="pin" class="input h-9 font-mono tracking-widest" inputmode="numeric" autocomplete="one-time-code" bind:value={pin} required />
				{#if filled}
					<p class="mt-1 text-xs text-ok">Filled in for you.</p>
				{:else}
					<details class="mt-1 text-xs text-faint">
						<summary class="cursor-pointer">Where do I find it?</summary>
						<ul class="mt-1.5 list-disc space-y-1 pl-4">
							<li><span class="text-muted">Windows:</span> in the black window that opened with the manager.</li>
							<li><span class="text-muted">Docker:</span> run <code class="font-mono text-muted">docker compose logs manager</code></li>
							<li><span class="text-muted">Linux service:</span> run <code class="font-mono text-muted">journalctl -u reskate-manager</code></li>
						</ul>
					</details>
				{/if}
			</div>
			<div>
				<label class="label" for="u">Username</label>
				<input id="u" class="input h-9" autocomplete="username" bind:value={username} required minlength="3" maxlength="32" pattern={usernamePattern} title="3-32 letters, digits and _ . -" />
			</div>
			<div class="grid grid-cols-2 gap-3">
				<div>
					<label class="label" for="p">Password</label>
					<input id="p" class="input h-9" type="password" autocomplete="new-password" bind:value={password} required minlength="10" />
				</div>
				<div>
					<label class="label" for="c">Confirm</label>
					<input id="c" class="input h-9" type="password" autocomplete="new-password" bind:value={confirm} required />
				</div>
			</div>
			<p class="text-xs text-faint">At least 10 characters.</p>
			{#if error}<p class="text-sm text-bad">{error}</p>{/if}
			<button class="btn btn-primary h-10 w-full text-base" disabled={busy}>{busy ? 'Creating…' : 'Create owner account'}</button>
		</form>
	</AuthShell>
{:else}
	<AuthShell wide tagline="one more thing." subtitle="Tell the manager how people get to this panel, so sign-in limits and the audit log can tell visitors apart.">
		<div class="card p-5">
			<AccessSettings suggest saveLabel="Finish" onsaved={() => goto('/')} onskip={() => goto('/')} />
		</div>
	</AuthShell>
{/if}
