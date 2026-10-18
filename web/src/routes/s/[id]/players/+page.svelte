<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api, lockTools, message, type Lock, type Player, type RoleList } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { instances } from '#lib/instances.svelte.js';
	import { newerVersion } from '#lib/thunderstore.svelte.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import { Busy } from '#lib/busy.svelte.js';
	import { ago, dateTime, playtime, plural } from '#lib/format.js';
	import { onLine, roleLine } from '#lib/lines.svelte.js';
	import { Paged } from '#lib/paged.svelte.js';
	import Icon from '#lib/components/Icon.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import RolePick from '#lib/components/RolePick.svelte';
	import ServerRoles from '#lib/components/ServerRoles.svelte';
	import SteamId from '#lib/components/SteamId.svelte';

	interface Seen {
		id: string;
		name: string;
		firstSeen: number;
		lastSeen: number;
		sessions: number;
		/** Seconds on this server; null for a player not seen since it was first kept. */
		playtime: number | null;
		/** The same on every server whose players the user sees, when that's more than this one. */
		playtimeAll?: number | null;
		/** Finished throwdowns played here, not counting quits, and the ones of them won. */
		throwdowns: number;
		wins: number;
		/** The same on every server whose players the user sees, as playtimeAll. */
		throwdownsAll?: number | null;
		winsAll?: number | null;
	}

	/** "12 · 5 won", or a dash for none. */
	const throwdowns = (played: number, won: number) => (played ? `${played}${won ? ` · ${won} won` : ''}` : '—');

	const ctx = useLive();
	const live = $derived(ctx.live);
	const players = $derived([...live.players].sort((a, b) => a.name.localeCompare(b.name)));
	const running = $derived(live.state?.state === 'running');
	const online = new Paged('players', () => players);

	let q = $state('');
	let history = $state.raw<Seen[] | null>(null);
	let historyError = $state('');
	const past = new Paged('history', () => history ?? []);
	const playtimeNote = 'Time on this server, counted since this manager began keeping it';
	const throwdownNote =
		"Throwdowns finished on this server, and won: the most points, or the last one left when everyone else quit. A quit counts for nothing. S.K.A.T.E. is won on letters, which the server never sees, so only by the last one left, with a landed try.";

	// Throwdowns are counted from lines the server writes only with its
	// activity log on.
	let activityOff = $state(false);
	$effect(() => {
		if (ctx.can('settings.view'))
			untrack(async () => {
				const r = await api.get<{ values: Record<string, unknown> }>(`/instances/${ctx.id}/settings`).catch(() => null);
				activityOff = r?.values.activity_log === false;
			});
	});
	const begin = newest();
	async function loadHistory() {
		const current = begin();
		try {
			const r = await api.get<Seen[]>(`/instances/${ctx.id}/history?q=${encodeURIComponent(q.trim())}`);
			if (!current()) return;
			history = r;
			past.page = 1;
			historyError = '';
		} catch (e) {
			if (current()) historyError = message(e);
		}
	}
	// The search runs on Enter, not on every keystroke in q.
	onMount(loadHistory);

	// A server with roles (ReSkate 2.0.6) has them managed at the top, and
	// given to a player from either list.
	let roles = $state.raw<RoleList | null>(null);
	const manage = $derived(ctx.can('ingame.admins.manage'));
	const withRoles = $derived(manage && !!roles?.hasRoles);
	const beginRoles = newest();
	async function loadRoles() {
		const current = beginRoles();
		const r = await api.get<RoleList>(`/instances/${ctx.id}/roles`).catch(() => roles);
		if (current()) roles = r;
	}
	$effect(() => {
		if (ctx.can('ingame.admins.manage')) untrack(loadRoles);
	});
	// Roles given in the game or from the console land in the file too.
	onLine(ctx.live, roleLine, () => {
		if (manage) loadRoles();
	});

	let banTarget = $state.raw<{ id: string; name: string } | null>(null);
	let banOpen = $state(false);

	// From ReSkate 2.0.8 a kick can keep a player out for a while instead of
	// until the server restarts: not offered where the server is known to be
	// older (a build the manager doesn't know may have it).
	const serverVersion = $derived(instances.byId(ctx.id)?.serverVersion);
	const timedKicks = $derived(!(serverVersion && newerVersion('2.0.8', serverVersion)));
	const kickTimes = [
		{ value: '', label: "The server's kick time" },
		{ value: '5', label: '5 minutes' },
		{ value: '15', label: '15 minutes' },
		{ value: '30', label: '30 minutes' },
		{ value: '60', label: '1 hour' },
		{ value: '360', label: '6 hours' },
		{ value: '1440', label: '24 hours' },
		{ value: '0', label: 'Until the server restarts' }
	];
	// ReSkate 2.1.0 locks tools for one player, kept when they leave, so the
	// list is the server's own (locks), asked again after each [lock] line.
	const toolLocks = $derived(running && !(serverVersion && newerVersion('2.1.0', serverVersion)));
	let locks = $state.raw<Lock[]>([]);
	const lockOf = (id: string) => locks.find((l) => l.id === id);
	const toolNames = (keys: string[]) => keys.map((k) => lockTools.find((t) => t.key === k)?.label ?? k).join(', ');
	const beginLocks = newest();
	async function loadLocks() {
		const current = beginLocks();
		const r = await api.get<Lock[]>(`/instances/${ctx.id}/locks`).catch(() => null);
		if (current()) locks = r ?? [];
	}
	$effect(() => {
		if (toolLocks) untrack(loadLocks);
		else locks = [];
	});
	onLine(ctx.live, /^\[lock\] /, () => {
		if (toolLocks) loadLocks();
	});
	let lockTarget = $state.raw<{ id: string; name: string } | null>(null);
	let lockOpen = $state(false);
	let lockPick = $state<string[]>([]);
	function askLock(id: string, name: string) {
		lockTarget = { id, name };
		lockPick = [...(lockOf(id)?.tools ?? [])];
		lockOpen = true;
	}
	// What's ticked is what stays locked: the newly ticked are locked, the
	// unticked given back.
	async function saveLock() {
		if (!lockTarget) return;
		const t = lockTarget;
		const had = lockOf(t.id)?.tools ?? [];
		const add = lockPick.filter((k) => !had.includes(k));
		const back = had.filter((k) => !lockPick.includes(k));
		lockOpen = false;
		const post = (verb: string, tools: string[]) =>
			attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/players/${t.id}/${verb}`, { tools }), (r) => r.reply);
		await busy.run(`lock:${t.id}`, async () => {
			if (add.length) await post('lock', add);
			if (back.length) await post('unlock', back);
		});
		loadLocks();
	}
	async function unlock(l: Lock) {
		await busy.run(`lock:${l.id}`, () => attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/players/${l.id}/unlock`, {}), (r) => r.reply));
		loadLocks();
	}

	let kickTarget = $state.raw<Player | null>(null);
	let kickOpen = $state(false);
	let kickFor = $state('');
	function askKick(p: Player) {
		kickTarget = p;
		kickFor = '';
		kickOpen = true;
	}

	const busy = new Busy();
	async function kick() {
		if (!kickTarget) return;
		const p = kickTarget;
		kickOpen = false;
		const body = timedKicks && kickFor ? { minutes: Number(kickFor) } : {};
		return busy.run(`kick:${p.id}`, () => attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/players/${p.id}/kick`, body), (r) => r.reply));
	}
	function askBan(id: string, name: string) {
		banTarget = { id, name };
		banOpen = true;
	}
	async function ban() {
		if (!banTarget) return;
		const t = banTarget;
		banOpen = false;
		await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/bans`, t), (r) => r.reply);
	}

	// A server from before roles has admins only.
	let adminTarget = $state.raw<Player | null>(null);
	let adminOpen = $state(false);
	function askAdmin(p: Player) {
		adminTarget = p;
		adminOpen = true;
	}
	async function makeAdmin() {
		if (!adminTarget) return;
		const t = adminTarget;
		adminOpen = false;
		await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/admins`, { id: t.id }), (r) => r.reply);
	}
</script>

<div class="space-y-4 p-4 sm:space-y-6 sm:p-6">
	{#if withRoles && roles}
		<ServerRoles {roles} seen={history ?? []} onchange={loadRoles} />
	{/if}

	<section class="card">
		<div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b-2 border-line px-4 py-3">
			<h2 class="flex items-center gap-2">Online now <span class="sticker sticker-accent">{players.length}</span></h2>
			{#if !running}<span class="text-xs text-faint">The server is not running.</span>{/if}
		</div>
		{#if !live.state}
			<Loading text="connecting…" class="px-4 py-8 text-center" />
		{:else if players.length}
			<div class="overflow-x-auto">
				<table class="table">
					<thead>
						<tr>
							<th>Player</th>{#if withRoles}<th>Role</th>{/if}<th class="hidden md:table-cell">SteamID64</th><th class="hidden sm:table-cell">Joined</th><th class="text-right">Actions</th>
						</tr>
					</thead>
					<tbody>
						{#each online.shown as p (p.id)}
							{@const lk = lockOf(p.id)}
							<tr>
								<td>
									<span class="font-medium">{p.name}</span>
									{#if lk}<span class="sticker sticker-pink ml-2" title="Locked: {toolNames([...lk.tools, ...lk.throwdown])}">locked</span>{/if}
									{#if !withRoles}
										{#if p.admin}<span class="sticker sticker-warn ml-2">Admin</span>{:else if p.role}<span class="sticker sticker-info ml-2">{p.role}</span>{/if}
									{/if}
									<SteamId id={p.id} small class="md:hidden" />
								</td>
								{#if withRoles && roles}<td><RolePick id={p.id} name={p.name} {roles} onchange={loadRoles} /></td>{/if}
								<td class="hidden md:table-cell"><SteamId id={p.id} /></td>
								<td class="hidden whitespace-nowrap text-muted sm:table-cell">{ago(p.joinedAt)}</td>
								<td>
									<div class="flex justify-end gap-1">
										{#if manage && roles && !roles.hasRoles && !p.admin}
											<button class="btn btn-sm btn-ghost" onclick={() => askAdmin(p)} title="Make in-game admin" aria-label="Make {p.name} an in-game admin"><Icon name="shield" size={14} /></button>
										{/if}
										{#if ctx.can('players.kick') && toolLocks}
											<button class="btn btn-sm btn-ghost" disabled={busy.is()} onclick={() => askLock(p.id, p.name)} title="Lock tools" aria-label="Lock tools for {p.name}"><Icon name="lock" size={14} /></button>
										{/if}
										{#if ctx.can('players.kick')}
											<button class="btn btn-sm" disabled={busy.is()} onclick={() => askKick(p)} aria-label="Kick {p.name}"><Icon name="kick" size={14} /> <span class="hidden sm:inline">Kick</span></button>
										{/if}
										{#if ctx.can('players.ban')}
											<button class="btn btn-sm btn-danger" onclick={() => askBan(p.id, p.name)} aria-label="Ban {p.name}"><Icon name="ban" size={14} /> <span class="hidden sm:inline">Ban</span></button>
										{/if}
									</div>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			<Pager paged={online} label="Players to show" />
		{:else}
			<p class="px-4 py-8 text-center font-marker text-faint">nobody's skating right now.</p>
		{/if}
	</section>

	{#if locks.length}
		<section class="card">
			<div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b-2 border-line px-4 py-3">
				<h2 class="flex items-center gap-2">Locked tools <span class="sticker sticker-pink">{locks.length}</span></h2>
				<span class="text-xs text-faint">Kept until unlocked or the server restarts, also for players who left.</span>
			</div>
			<div class="overflow-x-auto">
				<table class="table">
					<thead>
						<tr><th>Player</th><th class="hidden md:table-cell">SteamID64</th><th>Locked</th><th class="text-right">Actions</th></tr>
					</thead>
					<tbody>
						{#each locks as l (l.id)}
							<tr>
								<td>
									<span class="font-medium">{l.name}</span>
									{#if !l.here}<span class="sticker ml-2">not here</span>{/if}
									<SteamId id={l.id} small class="md:hidden" />
								</td>
								<td class="hidden md:table-cell"><SteamId id={l.id} /></td>
								<td class="text-sm">
									{#if l.tools.length}{toolNames(l.tools)}{/if}
									{#if l.throwdown.length}<span class="block text-xs text-muted">In their throwdown: {toolNames(l.throwdown)}</span>{/if}
								</td>
								<td>
									{#if ctx.can('players.kick')}
										<div class="flex justify-end gap-1">
											<button class="btn btn-sm btn-ghost" disabled={busy.is()} onclick={() => askLock(l.id, l.name)} title="Change" aria-label="Change what's locked for {l.name}"><Icon name="pencil" size={14} /></button>
											<button class="btn btn-sm" disabled={busy.is()} onclick={() => unlock(l)} aria-label="Unlock every tool for {l.name}"><Icon name="check" size={14} /> <span class="hidden sm:inline">Unlock</span></button>
										</div>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>
	{/if}

	<section class="card">
		<div class="flex flex-wrap items-center gap-3 border-b-2 border-line px-4 py-3">
			<h2>Player history</h2>
			<form
				class="relative w-full sm:ml-auto sm:w-72"
				onsubmit={(e) => {
					e.preventDefault();
					loadHistory();
				}}
			>
				<Icon name="search" size={14} class="absolute top-1/2 left-2 -translate-y-1/2 text-faint" />
				<input class="input pl-7" placeholder="Name or SteamID64, then Enter" aria-label="Search player history" bind:value={q} />
			</form>
			{#if activityOff}
				<p class="w-full text-xs text-muted">
					Throwdowns aren't being counted: <a class="text-accent hover:underline" href="/s/{ctx.id}/settings#gameplay">Log player activity</a> is off in this server's settings.
				</p>
			{/if}
		</div>
		{#if historyError}<LoadError class="m-4" error={historyError} onretry={loadHistory} />{/if}
		{#if history?.length}
			<div class="overflow-x-auto">
				<table class="table">
					<thead>
						<tr>
							<th>Player</th>{#if withRoles}<th>Role</th>{/if}<th class="hidden md:table-cell">SteamID64</th><th class="hidden lg:table-cell">First seen</th><th class="hidden sm:table-cell"
								>Last seen</th
							><th class="hidden sm:table-cell">Visits</th><th class="hidden sm:table-cell" title={playtimeNote}>Playtime</th
							><th class="hidden md:table-cell" title={throwdownNote}>Throwdowns</th><th></th>
						</tr>
					</thead>
					<tbody>
						{#each past.shown as h (h.id)}
							<tr>
								<td class="font-medium">
									{h.name}
									<SteamId id={h.id} small class="font-normal md:hidden" />
									{#if h.playtime != null}<span class="block text-xs font-normal text-muted sm:hidden">{playtime(h.playtime)} played</span>{/if}
									{#if h.throwdowns}<span class="block text-xs font-normal text-muted md:hidden">{plural(h.throwdowns, 'throwdown')}{h.wins ? ` · ${h.wins} won` : ''}</span>{/if}
								</td>
								{#if withRoles && roles}<td><RolePick id={h.id} name={h.name} {roles} onchange={loadRoles} /></td>{/if}
								<td class="hidden md:table-cell"><SteamId id={h.id} /></td>
								<td class="hidden text-muted lg:table-cell">{dateTime(h.firstSeen)}</td>
								<td class="hidden text-muted sm:table-cell">{dateTime(h.lastSeen)}</td>
								<td class="hidden tabular-nums sm:table-cell">{h.sessions}</td>
								<td class="hidden whitespace-nowrap tabular-nums sm:table-cell" title={h.playtime == null ? playtimeNote : undefined}>
									{playtime(h.playtime)}
									{#if h.playtimeAll != null && playtime(h.playtimeAll) !== playtime(h.playtime)}
										<span class="block text-xs text-faint" title="On every server whose players you can see">{playtime(h.playtimeAll)} on all servers</span>
									{/if}
								</td>
								<td class="hidden whitespace-nowrap tabular-nums md:table-cell">
									{throwdowns(h.throwdowns, h.wins)}
									{#if h.throwdownsAll != null && h.throwdownsAll !== h.throwdowns}
										<span class="block text-xs text-faint" title="On every server whose players you can see">{throwdowns(h.throwdownsAll, h.winsAll ?? 0)} on all servers</span>
									{/if}
								</td>
								<td>
									<div class="flex justify-end gap-1">
										{#if ctx.can('players.kick') && toolLocks}
											<button class="btn btn-sm btn-ghost" disabled={busy.is()} onclick={() => askLock(h.id, h.name)} title="Lock tools" aria-label="Lock tools for {h.name}"><Icon name="lock" size={14} /></button>
										{/if}
										{#if ctx.can('players.ban')}
											<button class="btn btn-sm btn-danger" onclick={() => askBan(h.id, h.name)} aria-label="Ban {h.name}"><Icon name="ban" size={14} /> <span class="hidden sm:inline">Ban</span></button>
										{/if}
									</div>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			<Pager paged={past} label="Players to show" />
		{:else}
			{#if history}
				<p class="px-4 py-6 text-sm text-faint">No players recorded{q ? ' matching that' : ' yet'}.</p>
			{:else if !historyError}
				<Loading class="px-4 py-6" />
			{/if}
		{/if}
	</section>
</div>

<Modal bind:open={kickOpen} title="Kick {kickTarget?.name || kickTarget?.id}?">
	{#if timedKicks}
		<p class="text-sm text-muted">They are taken off the server now, and told how long until they can join again.</p>
		<label class="mt-3 block">
			<span class="label">Keep them out for</span>
			<select class="input" bind:value={kickFor}>
				{#each kickTimes as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
			</select>
		</label>
		<p class="mt-1 text-xs text-faint">The server's kick time is under Settings &gt; Gameplay (ReSkate 2.0.8). A ban keeps them out for good.</p>
	{:else}
		<p class="text-sm text-muted">They are taken off the server now, and can't join again until it restarts.</p>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (kickOpen = false)}>Cancel</button>
		<button class="btn btn-danger" onclick={kick}><Icon name="kick" size={14} /> Kick</button>
	{/snippet}
</Modal>

<Modal bind:open={lockOpen} title="Lock tools for {lockTarget?.name || lockTarget?.id}">
	<p class="text-sm text-muted">
		Ticked tools are taken from them alone, at once, and stay taken if they leave and join again, until unlocked or the server restarts. Their role
		can't give one back.
	</p>
	<div class="mt-3 space-y-2">
		{#each lockTools as t (t.key)}
			<label class="flex cursor-pointer items-center gap-2 text-sm">
				<input type="checkbox" value={t.key} bind:group={lockPick} />
				{t.label}
			</label>
		{/each}
	</div>
	{#if lockTarget && lockOf(lockTarget.id)?.throwdown.length}
		<p class="mt-3 text-xs text-faint">Their throwdown also has {toolNames(lockOf(lockTarget.id)!.throwdown)} locked while it runs.</p>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (lockOpen = false)}>Cancel</button>
		<button class="btn btn-primary" onclick={saveLock}><Icon name="lock" size={14} /> Save</button>
	{/snippet}
</Modal>

<Modal bind:open={banOpen} title="Ban {banTarget?.name || banTarget?.id}?">
	<p class="text-sm text-muted">
		They are removed from the server now and cannot join again until unbanned. The ban is stored in this server's ReSkateServer.json.
	</p>
	{#snippet actions()}
		<button class="btn" onclick={() => (banOpen = false)}>Cancel</button>
		<button class="btn btn-danger" onclick={ban}><Icon name="ban" size={14} /> Ban</button>
	{/snippet}
</Modal>

<Modal bind:open={adminOpen} title="Make {adminTarget?.name || adminTarget?.id} an admin?">
	<p class="text-sm text-muted">
		They get in-game admin on this server straight away, including its admin commands. You can take it back under
		<a class="text-accent hover:underline" href="/s/{ctx.id}/bans">Bans &amp; admins</a>.
	</p>
	{#snippet actions()}
		<button class="btn" onclick={() => (adminOpen = false)}>Cancel</button>
		<button class="btn btn-primary" onclick={makeAdmin}><Icon name="shield" size={14} /> Make admin</button>
	{/snippet}
</Modal>
