<script lang="ts">
	import { untrack } from 'svelte';
	import { api, poolMapsFrom, type Mod, type ModList, type ServerPlugin, type SharedUse } from '#lib/api.js';
	import { sharedHelp, relinks, sharedDone } from '#lib/shared.js';
	import { useLive } from '#lib/context.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { packageOf, requires, withRequired } from '#lib/required.js';
	import { newest } from '#lib/newest.js';
	import { uploads, SHARED } from '#lib/uploads.svelte.js';
	import { ModsPage } from '#lib/mods.svelte.js';
	import { session } from '#lib/session.svelte.js';
	import { instances } from '#lib/instances.svelte.js';
	import { newerVersion } from '#lib/thunderstore.svelte.js';
	import { plural } from '#lib/format.js';
	import Icon from '#lib/components/Icon.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import ModDropZone from '#lib/components/ModDropZone.svelte';
	import ModTools from '#lib/components/ModTools.svelte';
	import FolderPath from '#lib/components/FolderPath.svelte';
	import OutdatedMods from '#lib/components/OutdatedMods.svelte';
	import ModCredits from '#lib/components/ModCredits.svelte';
	import ModUpdate from '#lib/components/ModUpdate.svelte';
	import ModProfiles from '#lib/components/ModProfiles.svelte';
	import SharedUsePicker from '#lib/components/SharedUsePicker.svelte';

	const ctx = useLive();
	const serverState = $derived(ctx.live.state?.state);
	const currentMap = $derived(serverState === 'running' ? (ctx.live.state?.info.map ?? '') : '');
	const serverName = $derived(ctx.live.state?.name ?? ctx.id);

	const mods = new ModsPage<ModList>(ctx.id, () => serverName);
	const list = $derived(mods.list);
	const updates = $derived(mods.updates);
	const load = () => mods.load();
	$effect(() => {
		serverState; // a running server also reports which maps it loaded
		uploads.installed[ctx.id]; // and a mod upload just landed
		uploads.installed[SHARED]; // here or in the shared mods
		untrack(load);
	});

	const lower = (s: string) => s.toLowerCase();
	const loaded = $derived(list?.loaded?.map(lower) ?? null);
	const isLoaded = (name: string) => !!loaded && loaded.includes(lower(name));
	const enabled = $derived((list?.mods ?? []).filter((m) => !m.disabled));
	// Maps the server will load on its next start, and loaded maps it will drop.
	const pending = $derived(loaded ? enabled.flatMap((m) => m.maps).filter((m) => !m.shadowed && !isLoaded(m.name)).length : 0);
	const stale = $derived.by(() => {
		if (!loaded || !list) return 0;
		const known = new Set([...list.retail, ...enabled.flatMap((m) => m.maps.filter((x) => !x.shadowed).map((x) => x.name))].map(lower));
		return loaded.filter((n) => !known.has(n)).length;
	});
	const restartNote = $derived(
		[pending && `${plural(pending, 'map')} to load`, stale && `${stale} to unload`].filter(Boolean).join(' and ')
	);
	const mapMods = $derived((list?.mods ?? []).filter((m) => m.maps.length || m.problem));
	// Park mods that add no maps are the Parks page's.
	const parkOnly = (m: Mod) => !m.maps.length && !m.problem && !!m.parks?.length;
	const otherMods = $derived((list?.mods ?? []).filter((m) => !m.maps.length && !m.problem && !parkOnly(m)));
	const parkMods = $derived((list?.mods ?? []).filter(parkOnly).length);

	const canUpload = $derived(ctx.can('settings.edit'));
	// From ReSkate 2.0.7 the running server reads Mods again on its maps reload:
	// not offered where the server is known to be older (a build the manager
	// doesn't know may have it).
	const serverVersion = $derived(instances.byId(ctx.id)?.serverVersion);
	const canReload = $derived(canUpload && !(serverVersion && newerVersion('2.0.7', serverVersion)));
	let reloading = $state(false);
	async function reloadMods() {
		reloading = true;
		await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/mods/reload`), (r) => r.reply.split('\n')[0] ?? r.reply);
		reloading = false;
		load();
	}

	// ---- ReSkate's plugins (2.1.2): which players must have, and how the server's run ----

	// Not offered where the server is known to be older.
	const pluginsHere = $derived(!(serverVersion && newerVersion('2.1.2', serverVersion)));
	// This server's and every server's, which the manager puts in its config as it starts.
	const required = $derived(list?.required ?? []);
	const every = $derived(list?.everyRequired ?? []);
	const ownReq = (mod: Mod) => !!mod.package && requires(required, mod.package);
	const everyReq = (mod: Mod) => !!mod.package && requires(every, mod.package);
	// On Thunderstore but not under Plugins: players' games (2.1.3) refuse its plugin, the server doesn't.
	const notPlugin = (mod: Mod) => mod.plugin && updates[mod.folder]?.plugin === false;
	// Everything players must have here: this server's, then every server's it doesn't have too, each with its mod here if there is one.
	const requiredRows = $derived(
		[...required.map((entry) => ({ entry, every: false })), ...every.filter((e) => !requires(required, e)).map((entry) => ({ entry, every: true }))].map((r) => ({
			...r,
			mod: list?.mods.find((m) => m.package && packageOf(m.package) === packageOf(r.entry))
		}))
	);
	async function setRequired(pkg: string, title: string, on: boolean, key = pkg) {
		working = key;
		const when = serverState === 'running' ? " from the server's next start" : '';
		await attempt(
			() => api.patch(`/instances/${ctx.id}/required-plugins`, { plugins: withRequired(required, pkg, on) }),
			on ? `Players are asked for ${title}${when}` : `Players no longer need ${title}${when}`
		);
		working = '';
		load();
	}

	// The running server's plugins, by folder, asked for while it has some.
	const runsPlugins = $derived(serverState === 'running' && pluginsHere && enabled.some((m) => m.serverPlugin));
	let pluginStates = $state.raw<Record<string, ServerPlugin>>({});
	const pluginOf = (mod: Mod) => (mod.serverPlugin && !mod.disabled ? pluginStates[lower(mod.folder)] : undefined);
	const beginPlugins = newest();
	async function loadPlugins() {
		const current = beginPlugins();
		const r = runsPlugins ? await api.get<ServerPlugin[]>(`/instances/${ctx.id}/server-plugins`).catch(() => null) : null;
		if (current()) pluginStates = Object.fromEntries((r ?? []).map((p) => [lower(p.id), p]));
	}
	$effect(() => {
		list; // a mod enabled, disabled or updated
		untrack(loadPlugins);
		if (!runsPlugins) return;
		// The server looks in its Plugins folder every 2 seconds.
		const t = setTimeout(loadPlugins, 3000);
		return () => clearTimeout(t);
	});
	async function pluginAction(mod: Mod, p: ServerPlugin, verb: 'reload' | 'off' | 'on') {
		working = mod.folder;
		await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/server-plugins/${encodeURIComponent(p.id)}/${verb}`), (r) => r.reply);
		working = '';
		loadPlugins();
	}

	// Linked shared mods are updated under Shared mods.
	const ownOutdated = $derived(mods.outdated.filter((m) => !m.shared));

	// Whether the map setting points at one of the mod's maps.
	const isCurrentMap = (mod: Mod) => !mod.disabled && !!list?.map && mod.maps.some((m) => lower(m.name) === lower(list!.map!));
	// The mod's maps in the map pool, which the server can't start without.
	const pooled = (mod: Mod) => (mod.disabled ? [] : poolMapsFrom(mod, list?.pool));
	const poolSpot = (name: string) => (list?.pool ?? []).findIndex((m) => lower(m) === lower(name)) + 1;
	const sharedPooled = $derived((list?.mods ?? []).filter((m) => m.shared).flatMap(pooled));

	let working = $state(''); // the folder being enabled, disabled or deleted
	let confirm = $state.raw<{ kind: 'delete' | 'disable' | 'share'; mod: Mod } | null>(null);
	let confirmOpen = $state(false);

	function ask(kind: 'delete' | 'disable' | 'share', mod: Mod) {
		confirm = { kind, mod };
		confirmOpen = true;
	}

	async function change(mod: Mod, request: () => Promise<unknown>, done: string) {
		confirmOpen = false;
		working = mod.folder;
		await attempt(request, done);
		working = '';
		load();
	}

	const path = (mod: Mod) => `/instances/${ctx.id}/mods/${encodeURIComponent(mod.folder)}`;
	const share = (mod: Mod) => change(mod, () => api.post(`${path(mod)}/share`), `Moved ${mod.title} to the shared mods`);

	// null when the manager has the shared mods turned off.
	const shared = $derived(list?.shared ?? null);
	// The shared mods reach every server that uses them, so moving one there
	// takes settings.edit on all servers.
	const canShare = $derived(canUpload && session.can('settings.edit') && !!shared && shared.use !== 'off');
	// The shared mods hold another copy of this folder, kept out by this one.
	const hasSharedCopy = (mod: Mod) => !mod.shared && !!shared?.own.includes(mod.folder);

	// Starting or stopping the shared mods adds or removes links, so it asks
	// first; switching between all and picked only changes how new ones arrive.
	let sharedNext = $state<SharedUse | null>(null);
	let sharedOpen = $state(false);
	let settingShared = $state(false);
	function chooseShared(use: SharedUse) {
		if (!shared || use === shared.use || settingShared) return;
		if (!relinks(shared.use, use)) return setShared(use);
		sharedNext = use;
		sharedOpen = true;
	}
	async function setShared(use: SharedUse) {
		sharedOpen = false;
		settingShared = true;
		await attempt(
			() => api.patch<{ swapped: string[] }>(`/instances/${ctx.id}/shared-mods`, { use }),
			(r) => sharedDone('This server', use, r.swapped.length)
		);
		settingShared = false;
		load();
	}
	const setEnabled = (mod: Mod, on: boolean) => change(mod, () => api.patch(path(mod), { enabled: on }), `${on ? 'Enabled' : 'Disabled'} ${mod.title}`);
	const remove = (mod: Mod) => change(mod, () => api.del(path(mod)), `Deleted ${mod.title}`);

	function toggle(mod: Mod) {
		if (!mod.disabled && (isCurrentMap(mod) || pooled(mod).length)) ask('disable', mod);
		else setEnabled(mod, !!mod.disabled);
	}
</script>

{#snippet credits(mod: Mod)}
	<ModCredits {mod} up={updates[mod.folder]}>
		{#if mod.shared}<span class="sticker sticker-info" title="Linked from the shared mods, which update and delete it">Shared</span>{/if}
		{#if mod.serverPlugin}<span class="sticker sticker-accent" title="The server runs its plugin too, while the mod is enabled (ReSkate's plugins)">Server plugin</span>
		{:else if mod.plugin}<span class="sticker" title="A Luau plugin that players' games run (ReSkate's plugins)">Plugin</span>{/if}
		{#if notPlugin(mod)}<span
				class="sticker sticker-warn"
				title="Thunderstore doesn't list it under Plugins, so players' games don't run its plugin (ReSkate 2.1.3){mod.serverPlugin ? '. The server still does' : ''}"
				>Not a Thunderstore plugin</span
			>{/if}
		{#if pluginsHere && everyReq(mod)}<span class="sticker sticker-info" title="Every server requires it of players; change that in the mod browser">Required on every server</span>
			<!-- Editors see it on the Require toggle. -->
		{:else if pluginsHere && ownReq(mod) && !canUpload}<span class="sticker sticker-info" title="Players must have it to join; their game gets it as they join and asks them to turn it on"
				>Required</span
			>{/if}
		{#if mod.disabled}<span class="sticker">Disabled</span>{/if}
	</ModCredits>
{/snippet}

{#snippet pluginState(mod: Mod)}
	{@const p = pluginOf(mod)}
	{#if p}
		<div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
			<span class="sticker {p.state === 'running' ? 'sticker-info' : 'sticker-warn'}">Plugin {p.state}</span>
			{#if p.state === 'running'}<span>{p.mb?.toFixed(1) ?? 0} MB, {p.tickMs?.toFixed(3) ?? 0} ms a tick</span>{/if}
			{#if canUpload}
				<!-- 2.1.3 refuses to reload a switched-off plugin; Switch on starts it -->
				{#if p.state !== 'switched off'}<button
						class="btn btn-ghost btn-sm"
						disabled={working === mod.folder}
						onclick={() => pluginAction(mod, p, 'reload')}
						title="Load its files again"><Icon name="restart" size={12} /> Reload</button
					>{/if}
				<button
					class="btn btn-ghost btn-sm"
					disabled={working === mod.folder}
					onclick={() => pluginAction(mod, p, p.state === 'switched off' ? 'on' : 'off')}
					title={p.state === 'switched off' ? 'Start it again' : 'Stop it until switched on or the server restarts; the mod stays enabled'}
					><Icon name="power" size={12} /> {p.state === 'switched off' ? 'Switch on' : 'Switch off'}</button
				>
			{/if}
		</div>
		{#if p.reason && p.state !== 'switched off'}<p class="mt-1 text-xs whitespace-pre-wrap text-bad">{p.reason}</p>{/if}
	{/if}
{/snippet}

{#snippet poolWarning(maps: string[])}
	<p class="flex items-start gap-1.5 text-warn">
		<Icon name="alert" size={14} class="mt-0.5" />
		<span
			>{maps.join(', ')}
			{maps.length === 1 ? 'is' : 'are'} in the map pool, and the server won't start until {maps.length === 1 ? "it's" : "they're"} taken out under
			<a class="underline" href="/s/{ctx.id}/settings">Settings</a>.</span
		>
	</p>
{/snippet}

{#snippet mapWarning(from: string)}
	<p class="flex items-start gap-1.5 text-warn">
		<Icon name="alert" size={14} class="mt-0.5" />
		<span>The server's map, {list?.map}, comes from {from}. Pick another under <a class="underline" href="/s/{ctx.id}/settings">Settings</a> before the server next starts.</span>
	</p>
{/snippet}

{#snippet controls(mod: Mod)}
	{@const updating = uploads.pending(ctx.id, mod.folder)}
	<div class="flex shrink-0 flex-wrap items-center gap-1.5">
		<!-- Not offered for a package players' games won't run, but one already required can be taken off. -->
		{#if canUpload && pluginsHere && mod.plugin && mod.package && !everyReq(mod) && (ownReq(mod) || !notPlugin(mod))}
			<button
				class="btn btn-sm"
				aria-pressed={ownReq(mod)}
				disabled={working === mod.folder || updating}
				onclick={() => setRequired(mod.package!, mod.title, !ownReq(mod), mod.folder)}
				title={ownReq(mod) ? 'Players no longer need it to join' : 'Players must have it to join: their game gets it as they join and asks them to turn it on'}
				><Icon name={ownReq(mod) ? 'check' : 'users'} size={12} /> {ownReq(mod) ? 'Required' : 'Require'}</button
			>
		{/if}
		<ModUpdate
			{mod}
			up={updates[mod.folder]}
			can={canUpload}
			{updating}
			onupdate={() => mods.update(mod)}
			elsewhere={mod.shared ? '/shared-mods' : undefined}
		/>
		{#if canUpload}
			<button class="btn btn-sm" disabled={working === mod.folder || updating} onclick={() => toggle(mod)}>
				<Icon name="power" size={12} />
				{mod.disabled ? 'Enable' : 'Disable'}
			</button>
			{#if mod.shared}
				<a class="btn btn-ghost btn-sm size-7 px-0" href="/shared-mods" title="Manage under Shared mods" aria-label="Manage {mod.title} under Shared mods"
					><Icon name="package" size={13} /></a
				>
			{:else}
				{#if canShare && !hasSharedCopy(mod)}
					<button
						class="btn btn-sm size-7 px-0"
						disabled={working === mod.folder || updating}
						onclick={() => ask('share', mod)}
						title="Move to the shared mods"
						aria-label="Move {mod.title} to the shared mods"><Icon name="package" size={13} /></button
					>
				{/if}
				<button
					class="btn btn-danger btn-sm size-7 px-0"
					disabled={working === mod.folder || updating}
					onclick={() => ask('delete', mod)}
					title="Delete"
					aria-label="Delete {mod.title}"><Icon name="trash" size={13} /></button
				>
			{/if}
		{/if}
	</div>
{/snippet}

<ModDropZone label="Mods" hint="Drop .zip files to add them as mods" enabled={canUpload} onfiles={(files) => mods.upload(files)}>
	<div class="flex flex-wrap items-end gap-3">
		<div class="w-full min-w-0 sm:w-auto sm:flex-1">
			<h1 class="page-title">Mods</h1>
			<p class="mt-2 text-sm text-muted">
				Custom maps come from mod folders next to the server. {#if canUpload}Zip a map mod's folder from the game's Mods folder and upload it (or drop it
					here){:else}Copy a map mod's folder from the game's Mods folder into the one below{/if}, then restart the server and pick the map under
				<a class="text-accent hover:underline" href="/s/{ctx.id}/settings">Settings</a>. Players need the same mod installed to join.
			</p>
		</div>
		<ModTools
			browse="/browse?to={encodeURIComponent(ctx.id)}"
			busy={mods.busy}
			{canUpload}
			uploading={mods.uploading}
			onrefresh={() => mods.load(true)}
			onfiles={(files) => mods.upload(files)}
		/>
	</div>

	{#if mods.error}<LoadError error={mods.error} onretry={load} />{/if}
	{#if list}
		<FolderPath label="Mods folder" folder={list.folder} />

		{#if shared}
			<section class="card flex flex-wrap items-center gap-x-4 gap-y-3 p-4">
				<div class="min-w-0 flex-1 basis-72">
					<div class="font-semibold"><a class="hover:underline" href="/shared-mods">Shared mods</a></div>
					<div class="text-sm text-muted">One copy on disk for every server that uses them. {sharedHelp(shared.use)}</div>
				</div>
				<SharedUsePicker use={shared.use} label="Shared mods on this server" disabled={!canUpload || settingShared} onchoose={chooseShared} />
			</section>
		{/if}

		<ModProfiles id={ctx.id} server={serverName} canAdd={canUpload} changed={list} />

		<section class="card">
			<div class="flex flex-wrap items-center gap-x-4 gap-y-2 border-b-2 border-line px-4 py-3">
				<div class="min-w-0 flex-1 basis-72">
					<h2>Plugins players must have</h2>
					<p class="text-sm text-muted">
						A player without one is asked once as they join, and their game gets it from Thunderstore. Then it asks them to turn each one on: until they do, it doesn't run.
					</p>
				</div>
				{#if canUpload}
					<a class="btn btn-sm" href="/browse?to={encodeURIComponent(ctx.id)}&category=Plugins"><Icon name="plus" size={12} /> Add from the mod browser</a>
				{/if}
			</div>
			{#if requiredRows.length}
				<ul class="divide-y divide-line/60 px-1 py-1.5">
					{#each requiredRows as r (r.entry)}
						<li class="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
							<span class="font-medium">{r.mod?.title ?? r.entry}</span>
							<span class="font-mono text-xs text-faint">{r.entry}</span>
							{#if r.every}<span class="sticker sticker-info" title="Change it in the mod browser">Every server</span>{/if}
							{#if r.mod}<span class="sticker" title="In this server's Mods{r.mod.serverPlugin ? ', and the server runs its plugin' : ''}">Installed here</span>{/if}
							{#if r.mod && notPlugin(r.mod)}<span class="sticker sticker-warn" title="Thunderstore doesn't list it under Plugins, so players' games get it but don't run its plugin"
									>Not a Thunderstore plugin</span
								>{/if}
							<span class="flex-1"></span>
							{#if canUpload && !r.every}
								<button
									class="btn btn-ghost btn-sm size-7 px-0"
									disabled={working === r.entry}
									onclick={() => setRequired(r.entry, r.mod?.title ?? r.entry, false)}
									title="Stop asking players for it"
									aria-label="Stop asking players for {r.entry}"><Icon name="x" size={13} /></button
								>
							{/if}
						</li>
					{/each}
				</ul>
			{:else}
				<p class="px-4 py-3 text-sm text-muted">None. Players can join without any plugin.</p>
			{/if}
			{#if !pluginsHere || list.requiredPending}
				<p class="flex items-start gap-1.5 border-t-2 border-line px-4 py-3 text-sm text-warn">
					<Icon name="alert" size={14} class="mt-0.5 shrink-0" />
					{!pluginsHere
						? `This server runs ReSkate ${serverVersion}: it asks for these once it's updated to 2.1.2 or later.`
						: 'These changed since the server started. Players are asked for them from its next start.'}
				</p>
			{/if}
		</section>

		{#if restartNote}
			<div class="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl border-2 border-dashed border-warn/60 bg-warn/5 px-4 py-3 text-sm text-muted">
				<p class="min-w-0 flex-1 basis-72">
					Mods changed since the server started: {restartNote}. Restart it to apply them{canReload ? ', or load them now' : ''}.
				</p>
				{#if canReload}
					<button class="btn btn-sm" disabled={reloading} onclick={reloadMods} title="The server reads its Mods folder again (ReSkate 2.0.7)"
						><Icon name="restart" size={14} /> Load them now</button
					>
				{/if}
			</div>
		{/if}

		<OutdatedMods
			outdated={mods.outdated}
			canUpdateAll={canUpload && ownOutdated.length > 1}
			busy={mods.updatingAll || ownOutdated.every((m) => uploads.pending(ctx.id, m.folder))}
			onupdateall={() => mods.updateAll()}
			error={mods.updatesError}
		/>

		{#if !mapMods.length}
			<p class="card p-4 text-sm text-muted">
				No map mods yet. A map mod is a folder with a <code>reskate-levels.json</code> inside, such as <code>Mods\bbcity\reskate-levels.json</code>. Find maps in
				the <a class="text-accent hover:underline" href="/browse?to={encodeURIComponent(ctx.id)}">mod browser</a>.
			</p>
		{/if}

		{#each mapMods as mod (mod.folder)}
			<section class="card">
				<div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b-2 border-line px-4 py-3">
					<div class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3 gap-y-1">
						<h2 class={mod.disabled ? 'text-muted' : ''}>{mod.title}</h2>
						{@render credits(mod)}
					</div>
					{@render controls(mod)}
				</div>
				<div class="space-y-3 px-4 py-3 {mod.disabled ? 'opacity-60' : ''}">
					{#if mod.title !== mod.folder}<p class="font-mono text-xs break-all text-faint">{mod.folder}</p>{/if}
					{#if hasSharedCopy(mod)}
						<p class="flex items-start gap-1.5 text-sm text-muted">
							<Icon name="info" size={14} class="mt-0.5" /> The shared mods hold another version of this folder. This server keeps its own until you delete it.
						</p>
					{/if}
					{#if mod.description}<p class="text-sm text-muted">{mod.description}</p>{/if}
					{@render pluginState(mod)}
					{#if mod.problem}
						<p class="flex items-start gap-1.5 text-sm text-bad"><Icon name="alert" size={14} /> {mod.problem}</p>
					{/if}
					{#if mod.maps.length}
						<ul class="divide-y divide-line/60 rounded-lg border-2 border-line">
							{#each mod.maps as m, i (i)}
								<li class="flex flex-wrap items-center gap-2 px-3 py-2">
									<span class="font-medium">{m.name}</span>
									<span class="min-w-0 flex-1 truncate font-mono text-xs text-faint" title={m.asset}>{m.asset}</span>
									{#if !m.shadowed && poolSpot(m.name)}
										<a class="sticker no-underline" href="/s/{ctx.id}/settings#maps" title="Number {poolSpot(m.name)} in the map pool">Pool #{poolSpot(m.name)}</a>
									{/if}
									{#if mod.disabled}
										{#if isLoaded(m.name)}<span class="sticker sticker-warn">Restart to unload</span>{/if}
									{:else if m.shadowed}
										<span class="sticker sticker-pink" title="Another mod or a retail map already registers this level, so the server ignores this one.">Duplicate</span>
									{:else if currentMap && lower(currentMap) === lower(m.name)}
										<span class="sticker sticker-accent">Current map</span>
									{:else if loaded && !isLoaded(m.name)}
										<span class="sticker sticker-warn">Restart to load</span>
									{:else if loaded}
										<span class="sticker sticker-info">Loaded</span>
									{/if}
								</li>
							{/each}
						</ul>
					{/if}
				</div>
			</section>
		{/each}

		{#if otherMods.length}
			<section class="card">
				<h2 class="border-b-2 border-line px-4 py-3">Other mods</h2>
				<p class="px-4 pt-3 text-xs text-faint">
					These have no reskate-levels.json, so they add no maps. Only players' games use them, apart from server plugins, which the server runs too.{pluginsHere
						? ' Require a plugin, and players without it get it as they join and are asked to turn it on.'
						: ''}
				</p>
				<ul class="divide-y divide-line/60 px-1 py-1.5">
					{#each otherMods as mod (mod.folder)}
						<li class="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2">
							<div class="min-w-0 flex-1">
								<div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
									<span class="font-medium {mod.disabled ? 'text-muted' : ''}">{mod.title}</span>
									{@render credits(mod)}
								</div>
								{#if mod.title !== mod.folder}<div class="truncate font-mono text-xs text-faint" title={mod.folder}>{mod.folder}</div>{/if}
								{@render pluginState(mod)}
							</div>
							{@render controls(mod)}
						</li>
					{/each}
				</ul>
			</section>
		{/if}

		{#if parkMods}
			<a class="card flex items-center gap-3 p-4 text-sm text-muted transition-colors hover:bg-raised" href="/s/{ctx.id}/parks">
				<Icon name="ramp" size={18} class="text-accent" />
				<span class="min-w-0 flex-1">{plural(parkMods, 'park mod')} {parkMods === 1 ? 'is' : 'are'} under <span class="font-semibold text-fg">Parks</span>, where they're set up and spawned.</span>
				<Icon name="chevron" size={14} class="-rotate-90" />
			</a>
		{/if}
	{:else if !mods.error}
		<Loading />
	{/if}
</ModDropZone>

<Modal
	bind:open={confirmOpen}
	title={confirm?.kind === 'delete' ? `Delete ${confirm.mod.title}?` : confirm?.kind === 'share' ? `Move ${confirm.mod.title} to the shared mods?` : `Disable ${confirm?.mod.title}?`}
>
	{#if confirm}
		{@const mod = confirm.mod}
		<div class="space-y-3 text-sm text-muted">
			{#if confirm.kind === 'delete'}
				<p>
					This deletes the <code>{mod.folder}</code> folder from the server for good.{hasSharedCopy(mod)
						? ' The shared copy takes its place.'
						: mod.disabled
							? ''
							: ' To keep it but stop the server loading it, disable it instead.'}
				</p>
			{:else if confirm.kind === 'share'}
				<p>
					The <code>{mod.folder}</code> folder moves to the shared mods, and this server links to it, {mod.disabled ? 'still disabled' : 'still enabled'}. Every
					server that uses the shared mods gets it at its next restart.
				</p>
				<p>Their own copies of the same version are replaced with links. Copies of other versions stay.</p>
			{/if}
			{#if confirm.kind !== 'share' && isCurrentMap(mod)}
				{@render mapWarning('this mod')}
			{/if}
			{#if confirm.kind !== 'share' && pooled(mod).length}
				{@render poolWarning(pooled(mod))}
			{/if}
			{#if serverState === 'running' && mod.maps.some((m) => isLoaded(m.name))}
				<p>The running server keeps its maps until it restarts, or until it reads its mods again (ReSkate 2.0.7).</p>
			{/if}
		</div>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (confirmOpen = false)}>Cancel</button>
		{#if confirm?.kind === 'delete'}
			<button class="btn btn-danger" onclick={() => confirm && remove(confirm.mod)}><Icon name="trash" size={14} /> Delete</button>
		{:else if confirm?.kind === 'share'}
			<button class="btn btn-primary" onclick={() => confirm && share(confirm.mod)}><Icon name="package" size={14} /> Move</button>
		{:else}
			<button class="btn btn-primary" onclick={() => confirm && setEnabled(confirm.mod, false)}>Disable</button>
		{/if}
	{/snippet}
</Modal>

<Modal bind:open={sharedOpen} title={sharedNext === 'off' ? 'Stop using the shared mods?' : 'Use the shared mods?'}>
	<div class="space-y-3 text-sm text-muted">
		{#if sharedNext === 'off'}
			<p>The links to the shared mods are taken out of this server's Mods folder. The shared mods stay, for every other server that uses them.</p>
			{#if list?.mods.some((m) => m.shared && isCurrentMap(m))}
				{@render mapWarning('a shared mod')}
			{/if}
			{#if sharedPooled.length}
				{@render poolWarning(sharedPooled)}
			{/if}
		{:else}
			<p>
				Every shared mod is linked into this server's Mods folder, {sharedNext === 'pick'
					? 'turned off; turn on the ones it should load'
					: 'turned on'}. Where the server has its own copy of the same version, the copy is replaced with a link; other versions stay.
			</p>
		{/if}
		{#if serverState === 'running'}<p>The running server picks up the change when it restarts.</p>{/if}
	</div>
	{#snippet actions()}
		<button class="btn" onclick={() => (sharedOpen = false)}>Cancel</button>
		<button class="btn btn-primary" onclick={() => sharedNext && setShared(sharedNext)}>{sharedNext === 'off' ? 'Stop using them' : 'Use them'}</button>
	{/snippet}
</Modal>
