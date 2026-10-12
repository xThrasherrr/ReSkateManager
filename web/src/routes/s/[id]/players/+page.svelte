<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api, message, type Player, type RoleList } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import { Busy } from '#lib/busy.svelte.js';
	import { ago, dateTime } from '#lib/format.js';
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
	import { ask } from '#lib/ask.svelte.js';

	interface Seen {
		id: string;
		name: string;
		firstSeen: number;
		lastSeen: number;
		sessions: number;
	}

	const ctx = useLive();
	const live = $derived(ctx.live);
	const players = $derived([...live.players].sort((a, b) => a.name.localeCompare(b.name)));
	const running = $derived(live.state?.state === 'running');
	const online = new Paged('players', () => players);

	let q = $state('');
	let history = $state.raw<Seen[] | null>(null);
	let historyError = $state('');
	const past = new Paged('history', () => history ?? []);
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

	const busy = new Busy();
	async function kick(p: Player) {
		if (!(await ask({ title: `Kick ${p.name}?`, body: 'They are taken off the server now, and can join again.', action: 'Kick' }))) return;
		return busy.run(`kick:${p.id}`, () => attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/players/${p.id}/kick`), (r) => r.reply));
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
							<tr>
								<td>
									<span class="font-medium">{p.name}</span>
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
										{#if ctx.can('players.kick')}
											<button class="btn btn-sm" disabled={busy.is()} onclick={() => kick(p)} aria-label="Kick {p.name}"><Icon name="kick" size={14} /> <span class="hidden sm:inline">Kick</span></button>
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
		</div>
		{#if historyError}<LoadError class="m-4" error={historyError} onretry={loadHistory} />{/if}
		{#if history?.length}
			<div class="overflow-x-auto">
				<table class="table">
					<thead>
						<tr>
							<th>Player</th>{#if withRoles}<th>Role</th>{/if}<th class="hidden md:table-cell">SteamID64</th><th class="hidden lg:table-cell">First seen</th><th class="hidden sm:table-cell"
								>Last seen</th
							><th class="hidden sm:table-cell">Visits</th><th></th>
						</tr>
					</thead>
					<tbody>
						{#each past.shown as h (h.id)}
							<tr>
								<td class="font-medium">
									{h.name}
									<SteamId id={h.id} small class="font-normal md:hidden" />
								</td>
								{#if withRoles && roles}<td><RolePick id={h.id} name={h.name} {roles} onchange={loadRoles} /></td>{/if}
								<td class="hidden md:table-cell"><SteamId id={h.id} /></td>
								<td class="hidden text-muted lg:table-cell">{dateTime(h.firstSeen)}</td>
								<td class="hidden text-muted sm:table-cell">{dateTime(h.lastSeen)}</td>
								<td class="hidden tabular-nums sm:table-cell">{h.sessions}</td>
								<td class="text-right">
									{#if ctx.can('players.ban')}
										<button class="btn btn-sm btn-danger" onclick={() => askBan(h.id, h.name)} aria-label="Ban {h.name}"><Icon name="ban" size={14} /> <span class="hidden sm:inline">Ban</span></button>
									{/if}
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
