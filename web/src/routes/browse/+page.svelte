<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { newest } from '#lib/newest.js';
	import type { Attachment } from 'svelte/attachments';
	import { page } from '$app/state';
	import { api, ApiError, type Mod, type ModList, type SharedModList, type StoreMod } from '#lib/api.js';
	import { thunderstore, modName, newerVersion, type MapsLook } from '#lib/thunderstore.svelte.js';
	import { uploads, SHARED } from '#lib/uploads.svelte.js';
	import { session } from '#lib/session.svelte.js';
	import { instances } from '#lib/instances.svelte.js';
	import { ago, bytes, httpUrl } from '#lib/format.js';
	import Icon from '#lib/components/Icon.svelte';
	import Modal from '#lib/components/Modal.svelte';

	const PAGE = 30;

	// The list is kept while the panel is open; coming back after the manager's
	// ten-minute cache has run out asks again.
	onMount(() => {
		if (!thunderstore.mods || Date.now() - thunderstore.fetched > 10 * 60_000) thunderstore.load();
	});

	// ---- Where mods can go: the servers the user may change, and the shared mods ----

	let sharedList = $state.raw<SharedModList | null>(null);
	let sharedOff = $state(false);
	// The shared mods reach every server that uses them, so adding to them takes settings.edit on all servers.
	const canShared = $derived(session.can('settings.edit') && !sharedOff);
	const servers = $derived(instances.list.filter((i) => session.can('settings.edit', i.id)));
	const canInstall = $derived(servers.length > 0 || canShared);
	// The server list is polled, so the lists below reload on a change to which servers there are, not on each poll.
	const serverIds = $derived(servers.map((s) => s.id).join('\n'));

	let lists = $state.raw<Record<string, ModList>>({});
	const beginServers = newest();
	async function loadServers(ids: string[]) {
		const current = beginServers();
		const got = await Promise.all(ids.map((id) => api.get<ModList>(`/instances/${id}/mods`).then((l): [string, ModList] => [id, l], () => null)));
		if (current()) lists = Object.fromEntries(got.filter((x) => x !== null));
	}
	const beginShared = newest();
	async function loadShared() {
		const current = beginShared();
		try {
			const l = await api.get<SharedModList>('/shared-mods');
			if (!current()) return;
			sharedList = l;
			sharedOff = false;
		} catch (e) {
			if (!current()) return;
			sharedList = null;
			if (e instanceof ApiError && e.status === 404) sharedOff = true;
		}
	}
	$effect(() => {
		const ids = serverIds ? serverIds.split('\n') : [];
		Object.values(uploads.installed); // a mod just landed somewhere
		untrack(() => {
			loadServers(ids);
			loadShared();
		});
	});

	// ---- What each place has already ----

	const of = (mods: Mod[] | undefined, mod: StoreMod) => (mods ?? []).filter((m) => m.package?.toLowerCase() === mod.fullName.toLowerCase());
	/** A server's own copy of the mod, its link to the shared one, and how it uses the shared mods (null: they are off in the manager). */
	function on(id: string, mod: StoreMod) {
		const ms = of(lists[id]?.mods, mod);
		return { own: ms.find((m) => !m.shared), link: ms.find((m) => m.shared), use: lists[id]?.shared?.use ?? null };
	}
	const inShared = (mod: StoreMod) => of(sharedList?.mods, mod)[0];

	/** The shared copy and the servers with their own, and whether any of them is older than the newest version. */
	function placed(mod: StoreMod) {
		const latest = mod.versions[0]?.version ?? '';
		const shared = inShared(mod);
		const own = servers.flatMap((s) => {
			const copy = on(s.id, mod).own;
			return copy ? [{ s, copy }] : [];
		});
		const outdated = (!!shared && newerVersion(latest, shared.version)) || own.some((o) => newerVersion(latest, o.copy.version));
		return { shared, own, outdated };
	}
	const installing = (mod: StoreMod) => [SHARED, ...servers.map((s) => s.id)].some((id) => uploads.installing(id, mod.fullName));

	// ---- Choosing where a version goes ----

	let toShared = $state(false);
	let toServers = $state<string[]>([]);

	// Where the last install went, so the next starts there. A link from a server's Mods page starts at that server.
	let asked = $state(page.url.searchParams.get('to'));
	function lastPlaces(): { shared: boolean; servers: string[] } {
		if (asked) return asked === 'shared' ? { shared: true, servers: [] } : { shared: false, servers: [asked] };
		try {
			const v = JSON.parse(localStorage.getItem('rsm.browse.places') ?? '');
			if (typeof v?.shared === 'boolean' && Array.isArray(v.servers)) return v;
		} catch {
			/* nothing kept, or storage is unavailable */
		}
		return servers[0] ? { shared: false, servers: [servers[0].id] } : { shared: canShared, servers: [] };
	}

	/** Starts the choice for a mod: its out-of-date copies if it has any, otherwise where the last install went. */
	function preset(mod: StoreMod) {
		const latest = mod.versions[0]?.version ?? '';
		const p = placed(mod);
		if (p.outdated) {
			toShared = !!p.shared && newerVersion(latest, p.shared.version);
			toServers = p.own.filter((o) => newerVersion(latest, o.copy.version)).map((o) => o.s.id);
		} else {
			const last = lastPlaces();
			toShared = last.shared;
			toServers = last.servers;
		}
	}

	/**
	 * One place a version can go, as the install list shows it. A server that
	 * uses the shared mods gets the version through them while they hold it, or
	 * are about to: one that loads every shared mod has nothing to tick, and a
	 * tick on any other turns its link on, in place of its own copy.
	 */
	interface Place {
		id: string;
		name: string;
		note: string; // what it has now, or how it gets the mod
		shared: boolean; // the shared mods, rather than a server
		checked: boolean;
		disabled: boolean;
	}

	function places(mod: StoreMod, version: string): Place[] {
		const shared = inShared(mod);
		const sharedSame = !!shared && shared.version === version;
		const viaShared = sharedSame || (canShared && toShared);
		const busy = (id: string) => uploads.installing(id, mod.fullName);
		const out: Place[] = [];
		if (canShared) {
			const note = shared ? `Has v${shared.version ?? '?'}` : 'One copy for every server that uses them';
			out.push({ id: SHARED, name: 'Shared mods', note, shared: true, checked: sharedSame || toShared, disabled: sharedSame || busy(SHARED) });
		}
		for (const s of servers) {
			const { own, link, use } = on(s.id, mod);
			const has = own ? `Has v${own.version ?? '?'}${own.disabled ? ', turned off' : ''}` : '';
			const tickable = (note: string) => out.push({ id: s.id, name: s.name, note, shared: false, checked: toServers.includes(s.id), disabled: busy(s.id) });
			const fixed = (note: string, checked: boolean) => out.push({ id: s.id, name: s.name, note, shared: false, checked, disabled: true });
			if (own?.version === version) {
				fixed(`Has v${version}${own.disabled ? ', turned off' : ''}`, true);
			} else if (viaShared && use && use !== 'off') {
				if (!own && ((link && !link.disabled) || (!link && use === 'all'))) fixed(`${sharedSame ? 'Loads' : 'Gets'} it from the shared mods`, true);
				else if (own) tickable(`${has}; tick to load the shared copy instead`);
				else if (link) tickable('The shared copy is turned off here; tick to load it');
				else tickable('Picks its shared mods; tick to load it');
			} else if (link && !link.disabled) {
				fixed(`Loads v${link.version ?? '?'} from the shared mods`, false);
			} else {
				tickable(has || 'Not installed');
			}
		}
		return out;
	}

	function tick(p: Place, checked: boolean) {
		if (p.shared) toShared = checked;
		else toServers = checked ? [...toServers.filter((x) => x !== p.id), p.id] : toServers.filter((x) => x !== p.id);
	}

	const chosen = (ps: Place[]) => ps.filter((p) => p.checked && !p.disabled);

	const title = (mod: StoreMod) => ({ fullName: mod.fullName, title: modName(mod) });

	/**
	 * Queues the version for every place ticked. The shared mods go first: the
	 * queue runs in order, so a server that uses them then links the new copy
	 * rather than downloading one of its own.
	 */
	function install(mod: StoreMod, version: string) {
		const ps = chosen(places(mod, version));
		for (const p of ps.toSorted((a, b) => Number(b.shared) - Number(a.shared))) uploads.install(p.id, p.name, title(mod), version);
		try {
			localStorage.setItem('rsm.browse.places', JSON.stringify({ shared: toShared, servers: toServers }));
		} catch {
			/* storage may be unavailable */
		}
		asked = null;
		detailOpen = false;
	}

	// ---- Finding mods ----

	let q = $state('');
	let category = $state('');
	let mapsOnly = $state(false);
	let sort = $state<'downloads' | 'updated' | 'created' | 'rating' | 'name'>('downloads');
	let showDeprecated = $state(false);
	let showNsfw = $state(false);

	const visible = $derived((thunderstore.mods ?? []).filter((m) => (showDeprecated || !m.deprecated) && (showNsfw || !m.nsfw)));
	const categories = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const m of visible) for (const c of m.categories) counts.set(c, (counts.get(c) ?? 0) + 1);
		return [...counts].sort((a, b) => b[1] - a[1]);
	});
	const matches = $derived.by(() => {
		const words = q.toLowerCase().split(/\s+/).filter(Boolean);
		return visible.filter((m) => {
			if (category && !m.categories.includes(category)) return false;
			const hay = `${modName(m)} ${m.owner} ${m.fullName} ${m.description}`.toLowerCase();
			return words.every((w) => hay.includes(w));
		});
	});
	const hasMaps = (look?: MapsLook) => look?.state === 'done' && look.maps.length > 0;
	const sorters: Record<typeof sort, (a: StoreMod, b: StoreMod) => number> = {
		downloads: (a, b) => b.downloads - a.downloads,
		updated: (a, b) => b.updated - a.updated,
		created: (a, b) => b.created - a.created,
		rating: (a, b) => b.rating - a.rating || b.downloads - a.downloads,
		name: (a, b) => modName(a).localeCompare(modName(b))
	};
	const listed = $derived((mapsOnly ? matches.filter((m) => hasMaps(thunderstore.maps(m))) : matches).toSorted(sorters[sort]));
	// Looking inside every match is what finding the maps among them takes.
	$effect(() => {
		if (!mapsOnly) return;
		const list = matches;
		untrack(() => list.forEach((m) => thunderstore.look(m)));
	});
	const looking = $derived(mapsOnly ? matches.filter((m) => thunderstore.maps(m)?.state === 'looking').length : 0);

	let shown = $derived.by(() => {
		q;
		category;
		mapsOnly;
		sort;
		showDeprecated;
		showNsfw;
		return PAGE;
	});
	const filtered = $derived(!!(q || category || mapsOnly));
	function clearFilters() {
		q = '';
		category = '';
		mapsOnly = false;
	}

	/** Looks inside a mod's zip for its maps once its card scrolls into view. */
	function lookWhenSeen(mod: StoreMod): Attachment {
		return (node) => {
			const io = new IntersectionObserver((entries) => {
				if (entries.some((e) => e.isIntersecting)) {
					thunderstore.look(mod);
					io.disconnect();
				}
			});
			io.observe(node);
			return () => io.disconnect();
		};
	}

	const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });

	// ---- The detail view ----

	let detail = $state.raw<StoreMod | null>(null);
	let detailOpen = $state(false);
	let version = $state('');
	// The version the details show: the one picked, or the newest.
	const shownVersion = $derived(detail ? (detail.versions.find((x) => x.version === version) ?? detail.versions[0]) : undefined);
	function openDetail(mod: StoreMod) {
		detail = mod;
		version = mod.versions[0]?.version ?? '';
		preset(mod);
		detailOpen = true;
	}
	$effect(() => {
		if (detailOpen && detail && version) untrack(() => thunderstore.look(detail!, version));
	});
	const byName = $derived(new Map((thunderstore.mods ?? []).map((m) => [m.fullName.toLowerCase(), m])));
	/** The package a dependency (Namespace-Name-1.2.3) names, if it is listed. */
	const dependency = (dep: string) => byName.get(dep.replace(/-[^-]*$/, '').toLowerCase());
</script>

{#snippet icon(mod: StoreMod, size: string)}
	{@const src = httpUrl(mod.icon)}
	{#if src}
		<img {src} alt="" loading="lazy" class="{size} shrink-0 rounded-lg border-2 border-ink bg-sunken object-cover" />
	{:else}
		<span class="{size} grid shrink-0 place-items-center rounded-lg border-2 border-ink bg-sunken text-faint"><Icon name="package" size={20} /></span>
	{/if}
{/snippet}

{#snippet mapsBadge(look: MapsLook | undefined)}
	{#if look?.state === 'done' && look.problem}
		<span class="sticker sticker-pink" title={look.problem}>Broken</span>
	{:else if look?.state === 'done' && look.maps.length}
		<span class="sticker sticker-accent" title={look.maps.map((m) => m.name).join(', ')}>{look.maps.length === 1 ? 'Map' : `${look.maps.length} maps`}</span>
	{:else if look?.state === 'done'}
		<span class="text-xs text-faint" title="It has no reskate-levels.json, so servers ignore it. Only players' games use it.">No maps</span>
	{:else if look?.state === 'unknown'}
		<span class="text-xs text-faint" title={look.reason}>Maps unknown</span>
	{/if}
{/snippet}

{#snippet flags(mod: StoreMod)}
	{#if mod.deprecated}<span class="sticker sticker-warn" title="Its author no longer supports it">Deprecated</span>{/if}
	{#if mod.nsfw}<span class="sticker sticker-pink">NSFW</span>{/if}
{/snippet}

<!-- Where the mod is already, and the button that opens the install list. -->
{#snippet action(mod: StoreMod)}
	{@const p = placed(mod)}
	{#if p.shared}<span class="sticker sticker-info" title="In the shared mods, v{p.shared.version ?? '?'}">Shared</span>{/if}
	{#if p.own.length}
		<span class="text-xs text-muted" title={p.own.map((o) => `${o.s.name}: v${o.copy.version ?? '?'}${o.copy.disabled ? ' (off)' : ''}`).join('\n')}>
			On {p.own.length === 1 ? p.own[0]?.s.name : `${p.own.length} servers`}
		</span>
	{/if}
	{#if canInstall}
		{#if installing(mod)}
			<button class="btn btn-sm" disabled>Installing…</button>
		{:else}
			<button class="btn btn-sm {p.outdated || (!p.shared && !p.own.length) ? 'btn-primary' : ''}" onclick={() => openDetail(mod)}>
				<Icon name="download" size={12} />
				{p.outdated ? 'Update' : 'Install'}
			</button>
		{/if}
	{/if}
{/snippet}

<div class="mx-auto max-w-6xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	<div class="flex flex-wrap items-end gap-3">
		<div class="w-full min-w-0 sm:w-auto sm:flex-1">
			<h1 class="page-title">Browse mods</h1>
			<p class="mt-2 text-sm text-muted">
				Mods from <a class="text-accent hover:underline" href="https://thunderstore.io/c/reskate/" target="_blank" rel="noreferrer">Thunderstore</a>, for any of
				your servers and the shared mods at once. Servers load only map mods; the rest change players' games, so the browser looks inside each mod for its maps.
			</p>
		</div>
		<button
			class="btn"
			disabled={thunderstore.loading}
			onclick={() => thunderstore.load(true)}
			title={thunderstore.fetched ? `Fetched from Thunderstore ${ago(thunderstore.fetched)}` : 'Fetch the list from Thunderstore'}><Icon name="restart" size={14} /> Refresh</button
		>
	</div>

	<section class="card space-y-3 p-3 sm:p-4">
		<label class="relative block">
			<span class="sr-only">Search mods</span>
			<Icon name="search" size={14} class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
			<input class="input pl-8" type="search" placeholder="Search by name, author or description" bind:value={q} />
		</label>
		<div class="flex flex-wrap items-center gap-x-4 gap-y-2.5">
			<label class="flex items-center gap-2 text-sm">
				<input type="checkbox" class="peer sr-only" bind:checked={mapsOnly} />
				<span class="switch"></span>
				<span>Maps only</span>
			</label>
			<label class="flex items-center gap-2 text-sm">
				<span class="sr-only">Category</span>
				<select class="input h-7 w-auto py-0 text-xs" bind:value={category}>
					<option value="">All categories</option>
					{#each categories as [c, n] (c)}<option value={c}>{c} ({n})</option>{/each}
				</select>
			</label>
			<label class="flex items-center gap-2 text-sm">
				<span class="text-muted">Sort</span>
				<select class="input h-7 w-auto py-0 text-xs" bind:value={sort}>
					<option value="downloads">Most downloaded</option>
					<option value="updated">Recently updated</option>
					<option value="created">Newest</option>
					<option value="rating">Top rated</option>
					<option value="name">Name</option>
				</select>
			</label>
			<span class="flex-1"></span>
			<label class="flex items-center gap-1.5 text-xs text-muted"><input type="checkbox" bind:checked={showDeprecated} /> Deprecated</label>
			<label class="flex items-center gap-1.5 text-xs text-muted"><input type="checkbox" bind:checked={showNsfw} /> NSFW</label>
		</div>
	</section>

	{#if !canInstall && thunderstore.mods}
		<p class="flex items-start gap-1.5 text-sm text-muted">
			<Icon name="info" size={14} class="mt-0.5" />
			{#if instances.loaded && !instances.list.length}
				<span>You can look around; there's no server to install mods on yet.{#if session.can('instances.manage')} <a href="/?new" class="text-accent hover:underline">Add one</a> first.{/if}</span>
			{:else}
				<span>You can look around, but installing a mod takes the right to change a server's settings.</span>
			{/if}
		</p>
	{/if}

	{#if thunderstore.error && !thunderstore.mods}
		<div class="card space-y-3 p-4 text-sm">
			<p class="text-bad">{thunderstore.error}</p>
			<button class="btn btn-sm" onclick={() => thunderstore.load(true)}><Icon name="restart" size={12} /> Try again</button>
		</div>
	{:else if !thunderstore.mods}
		<p class="card p-4 font-marker text-sm text-faint">fetching Thunderstore…</p>
	{:else}
		<div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted">
			<span>{listed.length} {listed.length === 1 ? 'mod' : 'mods'}{filtered ? ` of ${visible.length}` : ''}</span>
			{#if looking}
				<span class="flex items-center gap-1.5 text-xs text-faint">
					<span class="size-2 animate-pulse rounded-full bg-accent"></span> Looking inside {looking} more for maps…
				</span>
			{/if}
			{#if filtered}<button class="text-xs text-accent hover:underline" onclick={clearFilters}>Clear filters</button>{/if}
		</div>

		{#if !listed.length}
			<p class="card p-4 text-sm text-muted">
				{mapsOnly && looking ? 'No maps found yet; still looking.' : 'No mods match.'}
			</p>
		{/if}

		<div class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
			{#each listed.slice(0, shown) as mod (mod.fullName)}
				{@const v = mod.versions[0]}
				<article class="card flex flex-col overflow-hidden transition-[translate,box-shadow] hover:-translate-y-0.5 hover:shadow-sticker-lg" {@attach lookWhenSeen(mod)}>
					<button class="flex flex-1 gap-3 p-3 text-left" onclick={() => openDetail(mod)} aria-label="Details of {modName(mod)}">
						{@render icon(mod, 'size-16')}
						<div class="min-w-0 flex-1">
							<h2 class="truncate leading-tight" title={modName(mod)}>{modName(mod)}</h2>
							<div class="truncate text-xs text-muted">by {mod.owner} · v{v?.version}</div>
							<p class="mt-1 line-clamp-2 text-sm text-muted">{mod.description}</p>
						</div>
					</button>
					<div class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 border-t-2 border-line px-3 py-2">
						<span class="flex items-center gap-1 text-xs text-faint tabular-nums" title="{mod.downloads.toLocaleString()} downloads"
							><Icon name="download" size={11} />{compact.format(mod.downloads)}</span
						>
						<span class="text-xs text-faint tabular-nums">{bytes(v?.size)}</span>
						{@render mapsBadge(thunderstore.maps(mod))}
						{@render flags(mod)}
						<span class="flex-1"></span>
						{@render action(mod)}
					</div>
				</article>
			{/each}
		</div>

		{#if listed.length > shown}
			<div class="flex justify-center">
				<button class="btn" onclick={() => (shown += PAGE)}>Show more ({listed.length - shown} left)</button>
			</div>
		{/if}
	{/if}
</div>

<Modal bind:open={detailOpen} title={detail ? modName(detail) : ''} wide>
	{#if detail && shownVersion}
		{@const mod = detail}
		{@const v = shownVersion}
		{@const look = thunderstore.maps(mod, v.version)}
		<div class="space-y-4 text-sm">
			<div class="flex gap-4">
				{@render icon(mod, 'size-20')}
				<div class="min-w-0 flex-1 space-y-1.5">
					<div class="text-muted">by <span class="font-medium text-fg">{mod.owner}</span> · updated {ago(mod.updated)}</div>
					<div class="flex flex-wrap items-center gap-1.5">
						{#each mod.categories as c, i (i)}<span class="rounded-md bg-raised px-1.5 py-0.5 text-xs text-muted">{c}</span>{/each}
						{@render flags(mod)}
					</div>
					<div class="flex flex-wrap gap-3 text-xs">
						{#if httpUrl(mod.url)}<a class="flex items-center gap-1 text-accent hover:underline" href={httpUrl(mod.url)} target="_blank" rel="noreferrer"
								><Icon name="external" size={12} /> Thunderstore</a
							>{/if}
						{#if httpUrl(mod.website)}<a class="flex items-center gap-1 text-accent hover:underline" href={httpUrl(mod.website)} target="_blank" rel="noreferrer"
								><Icon name="external" size={12} /> Website</a
							>{/if}
					</div>
				</div>
			</div>

			{#if mod.description}<p class="text-muted">{mod.description}</p>{/if}

			<dl class="grid grid-cols-2 gap-2 sm:grid-cols-4">
				{#each [['Downloads', mod.downloads.toLocaleString()], ['Size', bytes(v.size)], ['Released', ago(v.created)], ['Rating', String(mod.rating)]] as [k, val] (k)}
					<div class="rounded-lg border-2 border-line bg-sunken px-2.5 py-1.5">
						<dt class="text-[11px] font-semibold tracking-wider text-faint uppercase">{k}</dt>
						<dd class="tabular-nums">{val}</dd>
					</div>
				{/each}
			</dl>

			<div>
				<div class="label">Maps in v{v.version}</div>
				{#if !look || look.state === 'looking'}
					<p class="flex items-center gap-1.5 text-faint"><span class="size-2 animate-pulse rounded-full bg-accent"></span> Looking inside the mod…</p>
				{:else if look.state === 'unknown'}
					<p class="flex items-start gap-1.5 text-faint"><Icon name="info" size={14} class="mt-0.5" /> {look.reason}.</p>
				{:else if look.state === 'failed'}
					<p class="flex items-start gap-1.5 text-faint">
						<Icon name="alert" size={14} class="mt-0.5" /> Could not look inside: {look.error}
						<button class="text-accent hover:underline" onclick={() => thunderstore.look(mod, v.version)}>Try again</button>
					</p>
				{:else if look.problem}
					<p class="flex items-start gap-1.5 text-bad"><Icon name="alert" size={14} class="mt-0.5" /> {look.problem}</p>
				{:else if look.maps.length}
					<ul class="divide-y divide-line/60 rounded-lg border-2 border-line">
						{#each look.maps as m, i (i)}
							<li class="flex flex-wrap items-center gap-2 px-3 py-1.5">
								<span class="font-medium">{m.name}</span>
								<span class="min-w-0 flex-1 truncate font-mono text-xs text-faint" title={m.asset}>{m.asset}</span>
							</li>
						{/each}
					</ul>
				{:else}
					<p class="text-muted">None. It has no reskate-levels.json, so servers ignore it; only players' games use it.</p>
				{/if}
			</div>

			{#if v.dependencies?.length}
				<div>
					<div class="label">Needs</div>
					<ul class="flex flex-wrap gap-1.5">
						{#each v.dependencies as dep, i (i)}
							{@const d = dependency(dep)}
							<li>
								{#if d}
									<button class="btn btn-sm" onclick={() => openDetail(d)}>{dep}</button>
								{:else}
									<span class="font-mono text-xs text-muted">{dep}</span>
								{/if}
							</li>
						{/each}
					</ul>
					<p class="mt-1 text-xs text-faint">Install these too; the manager does not fetch them for you yet.</p>
				</div>
			{/if}

			<label class="block">
				<span class="label">Version</span>
				<select class="input" bind:value={version}>
					{#each mod.versions as x, i (x.version)}
						<option value={x.version}>v{x.version}{i === 0 ? ' (newest)' : ''} · {bytes(x.size)} · {new Date(x.created).toLocaleDateString()}</option>
					{/each}
				</select>
			</label>

			{#if canInstall}
				<div>
					<div class="label">Install v{v.version} on</div>
					<ul class="divide-y divide-line/60 rounded-lg border-2 border-line">
						{#each places(mod, v.version) as p (p.id)}
							<li class="flex items-center gap-3 px-3 py-2">
								<input id="place-{p.id}" type="checkbox" class="shrink-0" checked={p.checked} disabled={p.disabled} onchange={(e) => tick(p, e.currentTarget.checked)} />
								<label for="place-{p.id}" class="min-w-0 flex-1 {p.disabled ? '' : 'cursor-pointer'}">
									<span class="flex items-center gap-1.5 font-medium">
										{#if p.shared}<Icon name="package" size={13} class="text-faint" />{/if}
										{p.name}
									</span>
									<span class="block text-xs text-muted">{p.note}</span>
								</label>
								{#if uploads.installing(p.id, mod.fullName)}<span class="text-xs text-faint">Installing…</span>{/if}
							</li>
						{/each}
					</ul>
					{#if canShared}
						<p class="mt-1 text-xs text-faint">
							A server that uses the shared mods loads the shared copy rather than downloading its own. Changes reach a server when it restarts.
						</p>
					{/if}
				</div>
			{/if}
		</div>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (detailOpen = false)}>Close</button>
		{#if detail && canInstall}
			{@const n = chosen(places(detail, version)).length}
			<button class="btn btn-primary" disabled={!n} onclick={() => detail && install(detail, version)}>
				<Icon name="download" size={14} />
				{n ? `Install v${version}${n > 1 ? ` in ${n} places` : ''}` : 'Tick where to install it'}
			</button>
		{/if}
	{/snippet}
</Modal>
