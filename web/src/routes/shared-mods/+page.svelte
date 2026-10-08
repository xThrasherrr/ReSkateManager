<script lang="ts">
	import { untrack } from 'svelte';
	import { api, poolMapsFrom, type Mod, type SharedModList, type SharedModsServer, type SharedUse } from '#lib/api.js';
	import { session } from '#lib/session.svelte.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { uploads, SHARED } from '#lib/uploads.svelte.js';
	import { ModsPage } from '#lib/mods.svelte.js';
	import { sharedHelp, relinks, sharedDone } from '#lib/shared.js';
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
	import SharedUsePicker from '#lib/components/SharedUsePicker.svelte';

	const mods = new ModsPage<SharedModList>(SHARED, () => 'Shared mods');
	const list = $derived(mods.list);
	const updates = $derived(mods.updates);
	const load = () => mods.load();
	$effect(() => {
		uploads.installed[SHARED]; // a shared mod just landed
		untrack(load);
	});

	// What reaches every server that uses the shared mods takes settings.edit on all of them.
	const canManage = $derived(session.can('settings.edit'));
	const servers = $derived(list?.servers ?? []);
	const using = $derived(servers.filter((s) => s.use !== 'off'));

	const lower = (s: string) => s.toLowerCase();
	// How a server that uses the shared mods has a mod: loading it, turned off,
	// keeping its own copy instead, or not linked until it next starts.
	function stateOn(s: SharedModsServer, mod: Mod): 'on' | 'off' | 'own' | 'next' {
		if (s.own.includes(mod.folder)) return 'own';
		if (s.enabled.includes(mod.folder)) return 'on';
		return s.disabled.includes(mod.folder) ? 'off' : 'next';
	}
	// Whether the server's map setting comes from the mod.
	const mapIn = (s: SharedModsServer, mod: Mod) => !!s.map && mod.maps.some((m) => lower(m.name) === lower(s.map!));
	const mapFrom = (mod: Mod) => using.filter((s) => stateOn(s, mod) === 'on' && mapIn(s, mod));
	// And the mod's maps in the server's map pool, which it can't start without.
	const poolFrom = (mod: Mod) => using.filter((s) => stateOn(s, mod) === 'on' && poolMapsFrom(mod, s.pool).length);

	let working = $state(''); // the mod being deleted, or "<server>/<mod>" being turned on or off
	let confirmMod = $state.raw<Mod | null>(null);
	let confirmOpen = $state(false);

	async function remove(mod: Mod) {
		confirmOpen = false;
		working = mod.folder;
		await attempt(() => api.del(`/shared-mods/${encodeURIComponent(mod.folder)}`), `Deleted ${mod.title}`);
		working = '';
		load();
	}

	// Turning a mod off where it is the server's map asks first.
	let offConfirm = $state.raw<{ s: SharedModsServer; mod: Mod } | null>(null);
	let offOpen = $state(false);
	function toggleOn(s: SharedModsServer, mod: Mod) {
		const on = stateOn(s, mod) !== 'on';
		if (!on && (mapIn(s, mod) || poolMapsFrom(mod, s.pool).length)) {
			offConfirm = { s, mod };
			offOpen = true;
			return;
		}
		setModOn(s, mod, on);
	}
	async function setModOn(s: SharedModsServer, mod: Mod, on: boolean) {
		offOpen = false;
		working = `${s.id}/${mod.folder}`;
		await attempt(
			() => api.patch(`/instances/${s.id}/mods/${encodeURIComponent(mod.folder)}`, { enabled: on }),
			`${mod.title} ${on ? 'loads on' : 'is off on'} ${s.name} from its next start`
		);
		working = '';
		load();
	}

	// Starting or stopping the shared mods on a server adds or removes links, so
	// it asks first; switching between all and picked only changes how new ones arrive.
	let toggling = $state('');
	let useChange = $state.raw<{ s: SharedModsServer; use: SharedUse } | null>(null);
	let useOpen = $state(false);
	function chooseUse(s: SharedModsServer, use: SharedUse) {
		if (use === s.use) return;
		if (!relinks(s.use, use)) return setUse(s, use);
		useChange = { s, use };
		useOpen = true;
	}
	async function setUse(s: SharedModsServer, use: SharedUse) {
		useOpen = false;
		toggling = s.id;
		await attempt(
			() => api.patch<{ swapped: string[] }>(`/instances/${s.id}/shared-mods`, { use }),
			(r) => sharedDone(s.name, use, r.swapped.length)
		);
		toggling = '';
		load();
	}
</script>

<ModDropZone label="Shared mods" hint="Drop .zip files to add them as shared mods" enabled={canManage} onfiles={(files) => mods.upload(files)}>
	<div class="flex flex-wrap items-end gap-3">
		<div class="w-full min-w-0 sm:w-auto sm:flex-1">
			<h1 class="page-title">Shared mods</h1>
			<p class="mt-2 text-sm text-muted">
				One copy of each mod for every server that uses them. Those servers link to these folders instead of keeping a copy each, and each mod can be turned on or
				off per server. Changes reach a server when it restarts.
			</p>
		</div>
		<ModTools
			browse="/browse?to=shared"
			busy={mods.busy}
			canUpload={canManage}
			uploading={mods.uploading}
			onrefresh={() => mods.load(true)}
			onfiles={(files) => mods.upload(files)}
		/>
	</div>

	{#if mods.error}<LoadError error={mods.error} onretry={load} />{/if}
	{#if list}
		<FolderPath label="Shared mods folder" folder={list.folder} />

		<section class="card">
			<h2 class="border-b-2 border-line px-4 py-3">Servers</h2>
			<ul class="divide-y divide-line/60">
				{#each servers as s (s.id)}
					<li class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
						<div class="min-w-0 flex-1 basis-60">
							<a class="font-medium hover:underline" href="/s/{s.id}/mods">{s.name}</a>
							<div class="text-xs text-faint">{sharedHelp(s.use)}</div>
						</div>
						<SharedUsePicker use={s.use} label="Shared mods on {s.name}" disabled={!s.canEdit || toggling === s.id} onchoose={(use) => chooseUse(s, use)} />
					</li>
				{:else}
					<li class="px-4 py-3 text-sm text-muted">No servers yet.{#if session.can('instances.manage')} <a href="/?new" class="text-accent hover:underline">Add one</a> on the dashboard.{/if}</li>
				{/each}
			</ul>
		</section>

		<OutdatedMods
			outdated={mods.outdated}
			canUpdateAll={canManage && mods.outdated.length > 1}
			busy={mods.updatingAll || mods.outdated.every((m) => uploads.pending(SHARED, m.folder))}
			onupdateall={() => mods.updateAll()}
			error={mods.updatesError}
		/>

		{#if !list.mods.length}
			<p class="card p-4 text-sm text-muted">
				No shared mods yet. {canManage
					? "Add one from the mod browser, upload a mod's zip here, or move one a server already has from that server's Mods page."
					: 'Someone with the right to change settings on every server can add them.'}
			</p>
		{/if}

		{#each list.mods as mod (mod.folder)}
			{@const updating = uploads.pending(SHARED, mod.folder)}
			<section class="card">
				<div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b-2 border-line px-4 py-3">
					<div class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3 gap-y-1">
						<h2>{mod.title}</h2>
						<ModCredits {mod} up={updates[mod.folder]} />
					</div>
					<div class="flex shrink-0 flex-wrap items-center gap-1.5">
						<ModUpdate {mod} up={updates[mod.folder]} can={canManage} {updating} onupdate={() => mods.update(mod)} />
						{#if canManage}
							<button
								class="btn btn-danger btn-sm size-7 px-0"
								disabled={working === mod.folder || updating}
								onclick={() => {
									confirmMod = mod;
									confirmOpen = true;
								}}
								title="Delete"
								aria-label="Delete {mod.title}"><Icon name="trash" size={13} /></button
							>
						{/if}
					</div>
				</div>
				<div class="space-y-3 px-4 py-3">
					{#if mod.title !== mod.folder}<p class="font-mono text-xs break-all text-faint">{mod.folder}</p>{/if}
					{#if mod.description}<p class="text-sm text-muted">{mod.description}</p>{/if}
					{#if mod.problem}
						<p class="flex items-start gap-1.5 text-sm text-bad"><Icon name="alert" size={14} /> {mod.problem}</p>
					{/if}
					{#if mod.maps.length}
						<p class="text-sm"><span class="text-faint">Maps:</span> {mod.maps.map((m) => m.name).join(', ')}</p>
					{:else if !mod.problem}
						<p class="text-xs text-faint">No reskate-levels.json, so servers ignore it. Only players' games use it.</p>
					{/if}
					{#if !using.length}
						<p class="text-sm text-muted">No server uses the shared mods yet. Choose to use them above, or on a server's Mods tab.</p>
					{:else}
						<div class="flex flex-wrap items-center gap-1.5" role="group" aria-label="Servers that load {mod.title}">
							{#each using as s (s.id)}
								{@const st = stateOn(s, mod)}
								{#if st === 'own'}
									<span class="btn btn-sm pointer-events-none opacity-60" title="{s.name} keeps its own copy, another version, in place of this one"
										>{s.name} · own copy</span
									>
								{:else if st === 'next'}
									<span class="btn btn-sm pointer-events-none opacity-60" title="Linked when {s.name} next starts">{s.name} · next start</span>
								{:else}
									<button
										class="btn btn-sm {st === 'on' ? 'btn-primary' : ''}"
										aria-pressed={st === 'on'}
										disabled={!s.canEdit || working === `${s.id}/${mod.folder}`}
										onclick={() => toggleOn(s, mod)}
										title={st === 'on' ? `Loaded on ${s.name}; click to turn it off there` : `Off on ${s.name}; click to load it there`}
									>
										<Icon name={st === 'on' ? 'check' : 'x'} size={12} />
										{s.name}
									</button>
								{/if}
							{/each}
						</div>
					{/if}
				</div>
			</section>
		{/each}
	{:else if !mods.error}
		<Loading />
	{/if}
</ModDropZone>

{#snippet poolWarning(s: SharedModsServer, mod: Mod)}
	{@const maps = poolMapsFrom(mod, s.pool)}
	{#if maps.length}
		<p class="flex items-start gap-1.5 text-warn">
			<Icon name="alert" size={14} class="mt-0.5" />
			<span
				>{maps.join(', ')}
				{maps.length === 1 ? 'is' : 'are'} in {s.name}'s map pool, and it won't start until {maps.length === 1 ? "it's" : "they're"} taken out under
				<a class="underline" href="/s/{s.id}/settings">its Settings</a>.</span
			>
		</p>
	{/if}
{/snippet}

<Modal bind:open={confirmOpen} title="Delete {confirmMod?.title}?">
	{#if confirmMod}
		{@const mod = confirmMod}
		{@const maps = mapFrom(mod)}
		<div class="space-y-3 text-sm text-muted">
			<p>
				This deletes <code>{mod.folder}</code> from the shared mods for good, and from every server that links to it, once each restarts. To keep it off one
				server only, turn it off for that server instead.
			</p>
			{#each maps as s (s.id)}
				<p class="flex items-start gap-1.5 text-warn">
					<Icon name="alert" size={14} class="mt-0.5" />
					<span>{s.name}'s map, {s.map}, comes from this mod. Pick another under <a class="underline" href="/s/{s.id}/settings">its Settings</a> before it next starts.</span>
				</p>
			{/each}
			{#each poolFrom(mod) as s (s.id)}
				{@render poolWarning(s, mod)}
			{/each}
		</div>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (confirmOpen = false)}>Cancel</button>
		<button class="btn btn-danger" onclick={() => confirmMod && remove(confirmMod)}><Icon name="trash" size={14} /> Delete</button>
	{/snippet}
</Modal>

<Modal bind:open={offOpen} title="Turn {offConfirm?.mod.title} off on {offConfirm?.s.name}?">
	{#if offConfirm}
		<div class="space-y-3 text-sm">
			{#if mapIn(offConfirm.s, offConfirm.mod)}
				<p class="flex items-start gap-1.5 text-warn">
					<Icon name="alert" size={14} class="mt-0.5" />
					<span
						>{offConfirm.s.name}'s map, {offConfirm.s.map}, comes from this mod. Pick another under <a class="underline" href="/s/{offConfirm.s.id}/settings"
							>its Settings</a
						> before it next starts.</span
					>
				</p>
			{/if}
			{@render poolWarning(offConfirm.s, offConfirm.mod)}
		</div>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (offOpen = false)}>Cancel</button>
		<button class="btn btn-primary" onclick={() => offConfirm && setModOn(offConfirm.s, offConfirm.mod, false)}>Turn it off</button>
	{/snippet}
</Modal>

<Modal bind:open={useOpen} title={useChange?.use === 'off' ? `Stop ${useChange.s.name} using the shared mods?` : `Use the shared mods on ${useChange?.s.name}?`}>
	{#if useChange}
		{@const { s, use } = useChange}
		<div class="space-y-3 text-sm text-muted">
			{#if use === 'off'}
				<p>The links to the shared mods are taken out of {s.name}'s Mods folder. The shared mods stay, for every other server that uses them.</p>
			{:else}
				<p>
					Every shared mod is linked into {s.name}'s Mods folder, {use === 'pick' ? 'turned off; turn on the ones it should load' : 'turned on'}. Where it has its own
					copy of the same version, the copy is replaced with a link; other versions stay.
				</p>
			{/if}
			<p>A running server picks up the change when it restarts.</p>
		</div>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (useOpen = false)}>Cancel</button>
		<button class="btn btn-primary" onclick={() => useChange && setUse(useChange.s, useChange.use)}>{useChange?.use === 'off' ? 'Stop using them' : 'Use them'}</button>
	{/snippet}
</Modal>
