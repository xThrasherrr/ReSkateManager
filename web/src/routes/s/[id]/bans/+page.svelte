<script lang="ts">
	import { untrack } from 'svelte';
	import { api, message } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import { Busy } from '#lib/busy.svelte.js';
	import { dateTime, steamProfile } from '#lib/format.js';
	import Icon from '#lib/components/Icon.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import { ask } from '#lib/ask.svelte.js';

	interface Ban {
		id: string;
		name: string;
		added: number;
	}
	interface Admin {
		id: string;
		name?: string;
		panel?: string; // kept in step for this panel user
	}

	const ctx = useLive();
	let bans = $state.raw<Ban[]>([]);
	let admins = $state.raw<Admin[]>([]);
	let banId = $state('');
	let banName = $state('');
	let adminId = $state('');
	let error = $state('');
	let loaded = $state(false);

	const begin = newest();
	async function load() {
		const current = begin();
		try {
			const b = await api.get<Ban[]>(`/instances/${ctx.id}/bans`);
			const a = ctx.can('ingame.admins.manage') ? await api.get<Admin[]>(`/instances/${ctx.id}/admins`) : [];
			if (!current()) return;
			bans = b;
			admins = a;
			error = '';
			loaded = true;
		} catch (e) {
			if (current()) error = message(e);
		}
	}
	$effect(() => {
		ctx.id;
		untrack(load);
	});
	// Changes made in game or from the console land in the file too; refresh when the server reports one.
	$effect(() => {
		const last = ctx.live.console.at(-1);
		if (last && /was (un)?banned|is (no longer )?an admin/.test(last.text)) untrack(load);
	});

	const steamOk = (s: string) => /^7656119\d{10}$/.test(s.trim());

	const busy = new Busy();
	async function addBan(e: SubmitEvent) {
		e.preventDefault();
		const body = { id: banId.trim(), name: banName.trim() };
		const r = await busy.run('ban', () => attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/bans`, body), (r) => r.reply));
		if (r) {
			banId = banName = '';
			load();
		}
	}
	async function unban(b: Ban) {
		if (!(await ask({ title: `Unban ${b.name || b.id}?`, body: 'They can join this server again.', action: 'Unban', danger: false }))) return;
		if (await busy.run(`unban:${b.id}`, () => attempt(() => api.del<{ reply: string }>(`/instances/${ctx.id}/bans/${b.id}`), (r) => r.reply))) load();
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
				<button class="btn btn-danger shrink-0" disabled={!steamOk(banId) || busy.is()}><Icon name="ban" size={14} /> {busy.is('ban') ? 'Banning…' : 'Ban'}</button>
			</form>
		{/if}
		{#if bans.length}
			<table class="table">
				<thead><tr><th>Player</th><th class="hidden sm:table-cell">SteamID64</th><th class="hidden md:table-cell">Banned</th><th></th></tr></thead>
				<tbody>
					{#each bans as b, i (i)}
						<tr>
							<td class="font-medium">{b.name || '—'}<a class="block font-mono text-[11px] font-normal text-faint hover:text-fg sm:hidden" href={steamProfile(b.id)} target="_blank" rel="noreferrer">{b.id}</a></td>
							<td class="hidden sm:table-cell"><a class="font-mono text-xs text-muted hover:text-fg" href={steamProfile(b.id)} target="_blank" rel="noreferrer">{b.id}</a></td>
							<td class="hidden text-muted md:table-cell">{dateTime(b.added)}</td>
							<td class="text-right">{#if ctx.can('players.ban')}<button class="btn btn-sm" disabled={busy.is()} onclick={() => unban(b)}>{busy.is(`unban:${b.id}`) ? 'Unbanning…' : 'Unban'}</button>{/if}</td>
						</tr>
					{/each}
				</tbody>
			</table>
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
					automatically through their linked Steam account.
				</p>
			</div>
			<form onsubmit={addAdmin} class="flex flex-col gap-2 border-b-2 border-line bg-sunken/40 px-4 py-3 sm:flex-row">
				<input class="input font-mono" placeholder="SteamID64" aria-label="SteamID64 to make an admin" bind:value={adminId} required />
				<button class="btn shrink-0" disabled={!steamOk(adminId) || busy.is()}><Icon name="plus" size={14} /> {busy.is('admin') ? 'Adding…' : 'Add admin'}</button>
			</form>
			{#if admins.length}
				<table class="table">
					<thead><tr><th>Player</th><th class="hidden sm:table-cell">SteamID64</th><th></th></tr></thead>
					<tbody>
						{#each admins as a, i (i)}
							<tr>
								<td class="font-medium">
									{a.name || '—'}
									{#if a.panel}<span class="ml-2 rounded bg-raised px-1.5 py-0.5 text-[10px] font-normal text-muted" title="Kept in step with this panel user's roles"
											>panel · {a.panel}</span
										>{/if}
									<a class="block font-mono text-[11px] font-normal text-faint hover:text-fg sm:hidden" href={steamProfile(a.id)} target="_blank" rel="noreferrer">{a.id}</a>
								</td>
								<td class="hidden sm:table-cell"><a class="font-mono text-xs text-muted hover:text-fg" href={steamProfile(a.id)} target="_blank" rel="noreferrer">{a.id}</a></td>
								<td class="text-right">{#if !a.panel}<button class="btn btn-sm" disabled={busy.is()} onclick={() => removeAdmin(a)}>{busy.is(`unadmin:${a.id}`) ? 'Removing…' : 'Remove'}</button>{/if}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{:else if loaded}
				<p class="px-4 py-6 text-sm text-faint">No in-game admins.</p>
			{:else if !error}
				<Loading class="px-4 py-6" />
			{/if}
		</section>
	{/if}
</div>
