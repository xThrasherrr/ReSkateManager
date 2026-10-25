<script lang="ts">
	import { onMount } from 'svelte';
	import { api, message, type Admin } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt, toastAnswer } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import { Busy } from '#lib/busy.svelte.js';
	import { dateTime } from '#lib/format.js';
	import { onLine } from '#lib/lines.svelte.js';
	import { Paged } from '#lib/paged.svelte.js';
	import { isSteamId } from '#lib/text.js';
	import Icon from '#lib/components/Icon.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import SteamId from '#lib/components/SteamId.svelte';
	import { ask, askTicked } from '#lib/ask.svelte.js';
	import { instances } from '#lib/instances.svelte.js';

	interface Ban {
		id: string;
		name: string;
		added: number;
	}

	const ctx = useLive();
	let bans = $state.raw<Ban[]>([]);
	let admins = $state.raw<Admin[]>([]);
	const bansPaged = new Paged('bans', () => bans);
	const adminsPaged = new Paged('admins', () => admins);
	let banId = $state('');
	let banName = $state('');
	let adminId = $state('');
	let error = $state('');
	let loaded = $state(false);

	const begin = newest();
	async function load() {
		const current = begin();
		try {
			const [b, a] = await Promise.all([
				api.get<Ban[]>(`/instances/${ctx.id}/bans`),
				ctx.can('ingame.admins.manage') ? api.get<Admin[]>(`/instances/${ctx.id}/admins`) : []
			]);
			if (!current()) return;
			bans = b;
			admins = a;
			error = '';
			loaded = true;
		} catch (e) {
			if (current()) error = message(e);
		}
	}
	onMount(load);
	// Changes made in game or from the console land in the file too; refresh when the server reports one.
	// From ReSkate 2.0.6 the admins are the players with the admin role.
	onLine(ctx.live, /was (un)?banned|is (no longer )?an admin|^\S.* (is now [a-z0-9_-]+|is [a-z0-9_-]+ again|no longer has a role)\.$/, load);

	const busy = new Busy();
	// Other servers this user could ban on too.
	const others = $derived(instances.list.filter((i) => i.id !== ctx.id && i.permissions?.includes('players.ban')).length);
	type Answer = { reply?: string; results?: { name: string; error?: string }[] };

	let everywhere = $state(false);
	async function addBan(e: SubmitEvent) {
		e.preventDefault();
		const body = { id: banId.trim(), name: banName.trim(), everywhere: everywhere && others > 0 };
		const r = await busy.run('ban', () => attempt(() => api.post<Answer>(`/instances/${ctx.id}/bans`, body)));
		if (r) {
			toastAnswer(r, `${body.name || body.id} banned`);
			banId = banName = '';
			load();
		}
	}
	async function unban(b: Ban) {
		const go = await askTicked({
			title: `Unban ${b.name || b.id}?`,
			body: 'They can join this server again.',
			action: 'Unban',
			danger: false,
			tick: others ? 'On every server that bans them' : undefined
		});
		if (!go) return;
		const r = await busy.run(`unban:${b.id}`, () => attempt(() => api.del<Answer>(`/instances/${ctx.id}/bans/${b.id}${go.ticked ? '?everywhere=1' : ''}`)));
		if (r) {
			toastAnswer(r, `${b.name || b.id} unbanned`);
			load();
		}
	}
	async function addAdmin(e: SubmitEvent) {
		e.preventDefault();
		const id = adminId.trim();
		if (await busy.run('admin', () => attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/admins`, { id }), (r) => r.reply))) {
			adminId = '';
			load();
		}
	}
	async function removeAdmin(a: Admin) {
		if (!(await ask({ title: `Remove ${a.name || a.id} as an admin?`, body: "They lose the server's admin commands straight away.", action: 'Remove' })))
			return;
		if (await busy.run(`unadmin:${a.id}`, () => attempt(() => api.del<{ reply: string }>(`/instances/${ctx.id}/admins/${a.id}`), (r) => r.reply))) load();
	}
</script>

<div class="grid gap-4 p-4 sm:gap-6 sm:p-6 xl:grid-cols-2">
	{#if error}<LoadError class="xl:col-span-2" {error} onretry={load} />{/if}
	<section class="card min-w-0 self-start">
		<div class="border-b-2 border-line px-4 py-3">
			<h2 class="flex items-center gap-2">Bans {#if loaded}<span class="sticker sticker-pink">{bans.length}</span>{/if}</h2>
			<p class="mt-0.5 text-xs text-faint">Banned SteamIDs cannot join. Kicks last only until the server restarts.</p>
		</div>
		{#if ctx.can('players.ban')}
			<form onsubmit={addBan} class="flex flex-col gap-2 border-b-2 border-line bg-sunken/40 px-4 py-3 sm:flex-row">
				<input class="input font-mono" placeholder="SteamID64" aria-label="SteamID64 to ban" bind:value={banId} required />
				<input class="input" placeholder="Name (optional)" aria-label="Their name (optional)" bind:value={banName} maxlength="64" />
				{#if others}
					<label class="inline-flex shrink-0 cursor-pointer items-center gap-2" title="Banned on every server you can ban on, where they aren't already">
						<input type="checkbox" class="peer sr-only" bind:checked={everywhere} />
						<span class="switch"></span>
						<span class="text-sm text-muted">Every server</span>
					</label>
				{/if}
				<button class="btn btn-danger shrink-0" disabled={!isSteamId(banId) || busy.is()}><Icon name="ban" size={14} /> {busy.is('ban') ? 'Banning…' : 'Ban'}</button>
			</form>
		{/if}
		{#if bans.length}
			<table class="table">
				<thead><tr><th>Player</th><th class="hidden sm:table-cell">SteamID64</th><th class="hidden md:table-cell">Banned</th><th></th></tr></thead>
				<tbody>
					{#each bansPaged.shown as b, i (i)}
						<tr>
							<td class="font-medium">{b.name || '—'}<SteamId id={b.id} small class="font-normal sm:hidden" /></td>
							<td class="hidden sm:table-cell"><SteamId id={b.id} /></td>
							<td class="hidden text-muted md:table-cell">{dateTime(b.added)}</td>
							<td class="text-right">{#if ctx.can('players.ban')}<button class="btn btn-sm" disabled={busy.is()} onclick={() => unban(b)}>{busy.is(`unban:${b.id}`) ? 'Unbanning…' : 'Unban'}</button>{/if}</td>
						</tr>
					{/each}
				</tbody>
			</table>
			<Pager paged={bansPaged} label="Bans to show" />
		{:else if loaded}
			<p class="px-4 py-6 text-sm text-faint">Nobody is banned.</p>
		{:else if !error}
			<Loading class="px-4 py-6" />
		{/if}
	</section>

	{#if ctx.can('ingame.admins.manage')}
		<section class="card min-w-0 self-start">
			<div class="border-b-2 border-line px-4 py-3">
				<h2 class="flex items-center gap-2">In-game admins {#if loaded}<span class="sticker sticker-warn">{admins.length}</span>{/if}</h2>
				<p class="mt-0.5 text-xs text-faint">
					They can run server commands from chat with /cmd. Panel users whose role includes “Be an in-game admin” are added and removed
					automatically through their linked Steam account. From ReSkate 2.0.6 they're the players with the server's admin role; every role is on the
					<a class="text-accent hover:underline" href="/s/{ctx.id}/players">Players</a> page.
				</p>
			</div>
			<form onsubmit={addAdmin} class="flex flex-col gap-2 border-b-2 border-line bg-sunken/40 px-4 py-3 sm:flex-row">
				<input class="input font-mono" placeholder="SteamID64" aria-label="SteamID64 to make an admin" bind:value={adminId} required />
				<button class="btn shrink-0" disabled={!isSteamId(adminId) || busy.is()}><Icon name="plus" size={14} /> {busy.is('admin') ? 'Adding…' : 'Add admin'}</button>
			</form>
			{#if admins.length}
				<table class="table">
					<thead><tr><th>Player</th><th class="hidden sm:table-cell">SteamID64</th><th></th></tr></thead>
					<tbody>
						{#each adminsPaged.shown as a, i (i)}
							<tr>
								<td class="font-medium">
									{a.name || '—'}
									{#if a.panel}<span class="ml-2 rounded bg-raised px-1.5 py-0.5 text-[10px] font-normal text-muted" title="Kept in step with this panel user's roles"
											>panel · {a.panel}</span
										>{/if}
									<SteamId id={a.id} small class="font-normal sm:hidden" />
								</td>
								<td class="hidden sm:table-cell"><SteamId id={a.id} /></td>
								<td class="text-right">{#if !a.panel}<button class="btn btn-sm" disabled={busy.is()} onclick={() => removeAdmin(a)}>{busy.is(`unadmin:${a.id}`) ? 'Removing…' : 'Remove'}</button>{/if}</td>
							</tr>
						{/each}
					</tbody>
				</table>
				<Pager paged={adminsPaged} label="Admins to show" />
			{:else if loaded}
				<p class="px-4 py-6 text-sm text-faint">No in-game admins.</p>
			{:else if !error}
				<Loading class="px-4 py-6" />
			{/if}
		</section>
	{/if}
</div>
