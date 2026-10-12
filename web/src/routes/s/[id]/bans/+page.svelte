<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api, message, type Admin, type RoleList, type ServerRole } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt, toast } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import { Busy } from '#lib/busy.svelte.js';
	import { dateTime, steamProfile } from '#lib/format.js';
	import { isSteamId } from '#lib/text.js';
	import Icon from '#lib/components/Icon.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import SteamId from '#lib/components/SteamId.svelte';
	import { ask } from '#lib/ask.svelte.js';
	import { instances } from '#lib/instances.svelte.js';

	interface Ban {
		id: string;
		name: string;
		added: number;
	}

	const ctx = useLive();
	let bans = $state.raw<Ban[]>([]);
	let admins = $state.raw<Admin[]>([]);
	let roles = $state.raw<RoleList | null>(null);
	let banId = $state('');
	let banName = $state('');
	let adminId = $state('');
	// Give a role: to whom, which, and on every server the user manages admins on.
	let roleId = $state('');
	let roleName = $state('');
	let everywhere = $state(false);
	// Players the server has seen, to pick from by name.
	let seen = $state.raw<{ id: string; name: string }[]>([]);
	let error = $state('');
	let loaded = $state(false);

	const begin = newest();
	async function load() {
		const current = begin();
		try {
			const [b, a, r] = await Promise.all([
				api.get<Ban[]>(`/instances/${ctx.id}/bans`),
				ctx.can('ingame.admins.manage') ? api.get<Admin[]>(`/instances/${ctx.id}/admins`) : [],
				ctx.can('ingame.admins.manage') ? api.get<RoleList>(`/instances/${ctx.id}/roles`) : null
			]);
			const h = r?.hasRoles && ctx.can('players.view') ? await api.get<{ id: string; name: string }[]>(`/instances/${ctx.id}/history`) : [];
			if (!current()) return;
			bans = b;
			admins = a;
			roles = r;
			seen = h;
			error = '';
			loaded = true;
		} catch (e) {
			if (current()) error = message(e);
		}
	}
	onMount(load);
	// Changes made in game or from the console land in the file too; refresh when the server reports one.
	// Lines come in batches (a ban's leave line right behind it), so look at every new one.
	const changed = /was (un)?banned|is (no longer )?an admin|^\S.* (is now [a-z0-9_-]+|is [a-z0-9_-]+ again|no longer has a role)\.$/;
	let lastSeq = ctx.live.console.at(-1)?.seq ?? 0;
	$effect(() => {
		const lines = ctx.live.console;
		const end = lines.at(-1)?.seq ?? 0;
		const restarted = end < lastSeq; // the manager restarted and counts from 0 again
		let hit = false;
		for (let i = lines.length - 1; i >= 0 && !hit; i--) {
			const e = lines[i]!;
			if (!restarted && e.seq <= lastSeq) break;
			hit = changed.test(e.text);
		}
		lastSeq = end;
		if (hit) untrack(load);
	});

	const busy = new Busy();

	// A role's tag as the game draws it: its own colour, with text that reads on it.
	function tagText(color = '') {
		const m = /^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(color);
		if (!m) return '';
		const c = (i: number) => parseInt(m[i] ?? '0', 16) / 255;
		return 0.2126 * c(1) + 0.7152 * c(2) + 0.0722 * c(3) > 0.5 ? '' : 'color: #fff;';
	}
	// What a role may use, in short; the whole of it is in the hover.
	function canUse(r: ServerRole) {
		const extra = r.commands.filter((c) => c !== '*');
		if (r.commands.includes('*')) return extra.length ? `every command, and ${extra.join(', ')}` : 'every command';
		if (!r.commands.length) return 'no commands';
		return r.commands.length > 5 ? `${r.commands.slice(0, 5).join(', ')} and ${r.commands.length - 5} more` : r.commands.join(', ');
	}
	const given = $derived(roles?.roles.reduce((n, r) => n + r.players.length, 0) ?? 0);
	// The roles to give: not the default, which everyone without one has already.
	const giveable = $derived((roles?.roles ?? []).filter((r) => r.name !== roles?.default));
	// Other servers this user could give the role on too.
	const others = $derived(instances.list.filter((i) => i.id !== ctx.id && i.permissions?.includes('ingame.admins.manage')).length);

	async function giveRole(e: SubmitEvent) {
		e.preventDefault();
		const body = { id: roleId.trim(), role: roleName, everywhere: everywhere && others > 0 };
		const r = await busy.run('role', () =>
			attempt(() => api.post<{ reply?: string; results?: { name: string; reply?: string; error?: string }[] }>(`/instances/${ctx.id}/roles/players`, body))
		);
		if (!r) return;
		if (r.results) {
			const done = r.results.filter((x) => !x.error);
			const left = r.results.filter((x) => x.error);
			if (done.length) toast.ok(`${body.role} given on ${done.map((x) => x.name).join(', ')}.`);
			for (const x of left) toast.error(`${x.name}: ${x.error}`);
		} else if (r.reply) toast.ok(r.reply);
		roleId = '';
		load();
	}
	async function takeRole(role: ServerRole, pl: Admin) {
		const who = pl.name || pl.id;
		if (!(await ask({ title: `Take ${who}'s ${role.name} role?`, body: `They're ${roles?.default || 'given no role'} again on this server.`, action: 'Take it' })))
			return;
		if (await busy.run(`role:${pl.id}`, () => attempt(() => api.del<{ reply: string }>(`/instances/${ctx.id}/roles/players/${pl.id}`), (r) => r.reply))) load();
	}

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
				<button class="btn btn-danger shrink-0" disabled={!isSteamId(banId) || busy.is()}><Icon name="ban" size={14} /> {busy.is('ban') ? 'Banning…' : 'Ban'}</button>
			</form>
		{/if}
		{#if bans.length}
			<table class="table">
				<thead><tr><th>Player</th><th class="hidden sm:table-cell">SteamID64</th><th class="hidden md:table-cell">Banned</th><th></th></tr></thead>
				<tbody>
					{#each bans as b, i (i)}
						<tr>
							<td class="font-medium">{b.name || '—'}<SteamId id={b.id} small class="font-normal sm:hidden" /></td>
							<td class="hidden sm:table-cell"><SteamId id={b.id} /></td>
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
					automatically through their linked Steam account. From ReSkate 2.0.6 they're the players with the server's admin role.
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
						{#each admins as a, i (i)}
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
			{:else if loaded}
				<p class="px-4 py-6 text-sm text-faint">No in-game admins.</p>
			{:else if !error}
				<Loading class="px-4 py-6" />
			{/if}
		</section>

		<section class="card min-w-0 xl:col-span-2">
			<div class="border-b-2 border-line px-4 py-3">
				<h2 class="flex items-center gap-2">Roles {#if roles?.hasRoles}<span class="sticker sticker-info">{given}</span>{/if}</h2>
				<p class="mt-0.5 text-xs text-faint">
					Who may do what in the game, and who was given each role, from the server's data/roles.json. Give one here or from the Players page, and
					take it away with the × by a player's name. README-roles.txt beside the server lists everything a role can have.
				</p>
			</div>
			{#if roles?.hasRoles}
				<form onsubmit={giveRole} class="flex flex-col gap-2 border-b-2 border-line bg-sunken/40 px-4 py-3 sm:flex-row sm:items-center">
					<input class="input font-mono" list="role-players" placeholder="SteamID64" aria-label="SteamID64 to give a role" bind:value={roleId} required />
					<datalist id="role-players">{#each seen as pl (pl.id)}<option value={pl.id}>{pl.name}</option>{/each}</datalist>
					<select class="input sm:w-44" aria-label="Role to give" bind:value={roleName} required>
						<option value="" disabled>Role…</option>
						{#each giveable as r (r.name)}<option value={r.name}>{r.name}</option>{/each}
					</select>
					{#if others}
						<label class="inline-flex shrink-0 cursor-pointer items-center gap-2" title="Each server has its own roles: it's given where a role of that name exists">
							<input type="checkbox" class="peer sr-only" bind:checked={everywhere} />
							<span class="switch"></span>
							<span class="text-sm text-muted">Every server</span>
						</label>
					{/if}
					<button class="btn shrink-0" disabled={!isSteamId(roleId) || !roleName || busy.is()}><Icon name="plus" size={14} /> {busy.is('role') ? 'Giving…' : 'Give role'}</button>
				</form>
				<table class="table">
					<thead><tr><th>Role</th><th class="hidden md:table-cell">Rank</th><th class="hidden sm:table-cell">Can use</th><th>Players</th></tr></thead>
					<tbody>
						{#each roles.roles as r (r.name)}
							<tr>
								<td class="whitespace-nowrap">
									<span class="font-medium">{r.name}</span>
									{#if r.tag}<span class="sticker ml-2" style="background-color: {r.color || '#FFFFFF'}; {tagText(r.color)}" title="Its tag in the game">{r.tag}</span>{/if}
									{#if r.name === roles.default}<span class="ml-2 rounded bg-raised px-1.5 py-0.5 text-[10px] text-muted" title="Everyone given no role has it">default</span>{/if}
								</td>
								<td class="hidden text-muted tabular-nums md:table-cell">{r.rank}</td>
								<td class="hidden text-sm text-muted sm:table-cell" title={[`Commands: ${r.commands.join(', ') || 'none'}`, `Permissions: ${r.permissions.join(', ') || 'none'}`, r.inherit ? '' : 'Only what it lists itself, not the default role’s'].filter(Boolean).join('\n')}>
									{canUse(r)}{#if !r.inherit}<span class="text-faint">{' · only its own'}</span>{/if}
								</td>
								<td>
									{#if r.name === roles.default}
										<span class="text-sm text-faint">everyone else</span>
									{:else if r.players.length}
										<div class="flex flex-wrap gap-1.5">
											{#each r.players as pl (pl.id)}
												<span class="inline-flex items-center rounded bg-raised text-xs">
													<a class="px-1.5 py-0.5 hover:text-fg {pl.name ? '' : 'font-mono text-[11px] text-muted'}" href={steamProfile(pl.id)} target="_blank" rel="noreferrer" title={pl.id}>{pl.name || pl.id}</a>
													{#if pl.panel}
														<span class="pr-1.5 text-[10px] text-faint" title="An admin kept in step with this panel user's roles">panel · {pl.panel}</span>
													{:else}
														<button
															class="px-1 text-faint hover:text-bad disabled:opacity-50"
															disabled={busy.is()}
															title="Take their role away"
															aria-label="Take {pl.name || pl.id}'s {r.name} role"
															onclick={() => takeRole(r, pl)}><Icon name="x" size={12} /></button
														>
													{/if}
												</span>
											{/each}
										</div>
									{:else}
										<span class="text-sm text-faint">nobody</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{:else if loaded}
				<p class="px-4 py-6 text-sm text-faint">No roles yet. ReSkate 2.0.6 and later write them to data/roles.json the first time they start.</p>
			{:else if !error}
				<Loading class="px-4 py-6" />
			{/if}
		</section>
	{/if}
</div>
