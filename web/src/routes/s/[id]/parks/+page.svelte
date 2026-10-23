<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api, ApiError, mapChoices, message, type Mod, type ModList, type ParkChat, type ParkPool, type ParksInfo } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { ask } from '#lib/ask.svelte.js';
	import { attempt, toast } from '#lib/toast.svelte.js';
	import { uploads, SHARED } from '#lib/uploads.svelte.js';
	import { ModsPage } from '#lib/mods.svelte.js';
	import { instances } from '#lib/instances.svelte.js';
	import { newerVersion } from '#lib/thunderstore.svelte.js';
	import { newest } from '#lib/newest.js';
	import { plural } from '#lib/format.js';
	import { shortName, shortNameRule } from '#lib/text.js';
	import { groundAxes, isFile, keyMaps, overlaps, suggestName, type ParkMod } from '#lib/parkmods.js';
	import Icon from '#lib/components/Icon.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import ModDropZone from '#lib/components/ModDropZone.svelte';
	import ModTools from '#lib/components/ModTools.svelte';
	import ModCredits from '#lib/components/ModCredits.svelte';
	import ModUpdate from '#lib/components/ModUpdate.svelte';

	const ctx = useLive();
	const serverState = $derived(ctx.live.state?.state);
	const running = $derived(serverState === 'running');
	const currentMap = $derived(running ? (ctx.live.state?.info.map ?? '') : '');
	const serverName = $derived(ctx.live.state?.name ?? ctx.id);
	const canEdit = $derived(ctx.can('settings.edit'));
	const serverVersion = $derived(instances.byId(ctx.id)?.serverVersion);
	const tooOld = $derived(!!serverVersion && newerVersion('2.0.6', serverVersion));

	// The mods, for their parks, credits and updates; the Parks API for what
	// the server and the manager make of them.
	const mods = new ModsPage<ModList>(ctx.id, () => serverName);
	let info = $state.raw<ParksInfo | null>(null);
	let error = $state('');
	const begin = newest();
	async function loadInfo() {
		const current = begin();
		try {
			const i = await api.get<ParksInfo>(`/instances/${ctx.id}/parks`);
			if (current()) [info, error] = [i, ''];
		} catch (e) {
			if (current()) error = message(e);
		}
	}
	const load = () => {
		mods.load();
		loadInfo();
	};
	$effect(() => {
		serverState; // a map loading on start puts a pool up
		ctx.live.state?.info.map; // and so does a map change
		uploads.installed[ctx.id]; // a park mod upload just landed
		uploads.installed[SHARED];
		untrack(load);
		// The pool goes up a moment after the map is in.
		const t = setTimeout(loadInfo, 4000);
		return () => clearTimeout(t);
	});
	// A timed park counts down, and goes by itself. Each look is a line in
	// the server's own log, so not too often.
	onMount(() => {
		const t = setInterval(() => !document.hidden && running && loadInfo(), 60_000);
		return () => clearInterval(t);
	});

	const lower = (s: string) => s.toLowerCase();
	const same = (a: string, b: string) => lower(a) === lower(b);
	const parkMods = $derived((mods.list?.mods ?? []).filter((m) => m.parks?.length));
	// What's at hand first: parks set up for the map that runs, then ones to
	// set up, then other maps', then mods that can't spawn anything.
	function rank(m: Mod) {
		if (m.disabled || m.parks!.every((k) => fileOf(m.folder, k)?.problem)) return 3;
		const here = m.parks!.flatMap((k) => setupsOf(m, k));
		if (currentMap && here.some((p) => same(p.map, currentMap))) return 0;
		return here.length ? 2 : 1;
	}
	const sortedMods = $derived(parkMods.map((m) => [rank(m), m] as const).sort((a, b) => a[0] - b[0]).map(([, m]) => m));
	const setup = $derived(info?.setup ?? []);
	const spawned = $derived(new Map((info?.spawned ?? []).map((s) => [s.name, s])));
	const upObjects = $derived([...spawned.values()].reduce((n, s) => n + s.objects, 0));
	const fileOf = (folder: string, key: string) => info?.parks.find((p) => p.folder === folder && p.key === key);
	const setupsOf = (mod: Mod, key: string) => setup.filter((p) => isFile(p, mod.folder, key, mod.parks!.length));
	// What a set-up park reads: its mod's file, if the server loads that mod.
	function fileFor(p: ParkMod) {
		const mod = parkMods.find((m) => !m.disabled && m.folder === p.folder);
		const key = mod?.parks!.find((k) => isFile(p, mod.folder, k, mod.parks!.length));
		return key ? fileOf(p.folder, key) : undefined;
	}
	// Set up, but its mod or file isn't in Mods: the server can't spawn it.
	const missing = $derived(setup.filter((p) => !parkMods.some((m) => !m.disabled && m.parks!.some((k) => isFile(p, m.folder, k, m.parks!.length)))));
	const maps = $derived(mods.list ? mapChoices(mods.list) : null);
	const mapNames = $derived(maps ? [...maps.retail, ...maps.mods.flatMap((m) => m.maps.map((x) => x.name))] : Object.values(keyMaps));

	// ---- setting parks up: the server's park_mods, through the settings API ----

	let working = $state(''); // what's being changed, so its buttons wait
	async function saveSetup(next: ParkMod[], done: string) {
		try {
			const r = await api.post<{ notApplied?: string[]; results?: { reply: string; error?: string }[] }>(`/instances/${ctx.id}/settings`, {
				values: { park_mods: next }
			});
			// A running server sets it up live, and says why when it won't.
			const why = r.results?.map((x) => x.error || x.reply).at(-1);
			if (r.notApplied?.length) toast.error(`The server didn't take it${why ? `: ${why}` : '.'}`);
			else toast.ok(done);
		} catch (e) {
			toast.error(e);
		}
		await loadInfo();
	}

	// A row's set-up form, as edited; untouched, it offers the suggestion.
	let drafts = $state<Record<string, { name?: string; map?: string; open?: boolean }>>({});
	const rowId = (mod: Mod, key: string) => `${mod.folder}/${key}`;
	const draftName = (mod: Mod, key: string) => drafts[rowId(mod, key)]?.name ?? suggestName(mod.folder, key, mod.parks!.length > 1, setup.map((p) => p.name));
	const draftMap = (key: string, mod: Mod) => drafts[rowId(mod, key)]?.map ?? keyMaps[lower(key)] ?? mapNames[0] ?? '';
	function nameProblem(name: string) {
		if (!shortName.test(name)) return shortNameRule;
		if (setup.some((p) => p.name === name)) return `A park is called ${name} already.`;
		return '';
	}
	async function setUp(mod: Mod, key: string) {
		const id = rowId(mod, key);
		const name = draftName(mod, key);
		working = id;
		await saveSetup([...setup, { name, folder: mod.folder, map: draftMap(key, mod), key: lower(key) }], `Set up ${name}`);
		delete drafts[id];
		working = '';
	}
	async function takeOut(p: ParkMod) {
		const pools = (info?.pools ?? []).filter((pool) => pool.parks.includes(p.name));
		const ok = await ask({
			title: `Take ${p.name} out?`,
			body: `The server forgets the park ${p.name}${spawned.has(p.name) ? ' and takes it down' : ''}. Its mod stays in Mods, to set up again.${pools.length ? ` It leaves ${plural(pools.length, 'pool')} too.` : ''}`,
			action: 'Take out'
		});
		if (!ok) return;
		working = p.name;
		// The pools first, while it's still set up: they may not name a park that isn't.
		if (pools.length && info) {
			const next = info.pools.map((pool) => ({ ...pool, parks: pool.parks.filter((n) => n !== p.name) })).filter((pool) => pool.parks.length);
			if (!(await saveParks(next, info.chat))) return void (working = '');
		}
		await saveSetup(setup.filter((x) => x !== p), `Took ${p.name} out`);
		working = '';
	}

	// ---- spawning ----

	let spawnFor = $state(0); // minutes; 0 until removed
	const durations = [
		[0, 'until removed'],
		[15, 'for 15 min'],
		[30, 'for 30 min'],
		[60, 'for 1 hour'],
		[120, 'for 2 hours']
	] as const;
	const firstLine = (r: { reply: string }) => (r.reply.split('\n')[0] ?? '').replace(/^\[parks\] /, '').replace(/ by .*$/, '');
	async function spawn(names: string[], minutes = spawnFor) {
		working = names.join(' ');
		await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/parks/spawn`, { names, minutes }), firstLine);
		working = '';
		loadInfo();
	}
	async function takeDown(names: string[]) {
		working = names.join(' ');
		await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/parks/remove`, { names }), firstLine);
		working = '';
		loadInfo();
	}
	const spawnable = (p: ParkMod) => running && same(p.map, currentMap);

	// ---- the limits: settings that need a restart ----

	let limitsOpen = $state(false);
	let limits = $state({ parks: 3, objects: 512 });
	function editLimits() {
		if (info) limits = { ...info.limits };
		limitsOpen = true;
	}
	async function saveLimits(restart = false) {
		const values = { 'park_limits.parks': limits.parks, 'park_limits.objects': limits.objects };
		try {
			await api.post(`/instances/${ctx.id}/settings`, { values, restart });
			toast.ok(restart ? 'Saved; the server restarted to apply it.' : 'Saved.');
			limitsOpen = false;
			loadInfo();
		} catch (e) {
			if (e instanceof ApiError && e.status === 409 && Array.isArray(e.body.restartKeys)) {
				const yes = await ask({
					title: 'Restart the server?',
					body: 'The server reads its park limits as it starts. Restarting now takes everyone off for a moment, and the parks that are up go too; a pool goes up again as it starts.',
					action: 'Restart'
				});
				if (yes) await saveLimits(true);
			} else toast.error(e);
		}
	}
	const limitsOk = $derived(Number.isInteger(limits.parks) && limits.parks >= 1 && limits.parks <= 16 && Number.isInteger(limits.objects) && limits.objects >= 1 && limits.objects <= 1024);

	// ---- the manager's own: pools, and who may use /parks ----

	async function saveParks(pools: ParkPool[], chat: ParkChat) {
		try {
			info = { ...info!, ...(await api.patch<{ pools: ParkPool[]; chat: ParkChat }>(`/instances/${ctx.id}/parks`, { pools, chat })) };
			return true;
		} catch (e) {
			toast.error(e);
			return false;
		}
	}

	const poolMaps = $derived([...new Set([...setup.map((p) => p.map), ...(info?.pools ?? []).map((p) => p.map)])]);
	const poolsByMap = $derived(poolMaps.map((m) => [m, (info?.pools ?? []).map((p, i) => ({ ...p, i })).filter((p) => same(p.map, m))] as const).filter(([, l]) => l.length));
	// The pairs of a pool's parks whose boxes come within reach of each other.
	function clashes(names: string[]) {
		const boxes = names.map((n) => [n, setup.find((p) => p.name === n)] as const).flatMap(([n, p]) => (p && fileFor(p) ? [[n, fileFor(p)!.box] as const] : []));
		const out: [string, string][] = [];
		boxes.forEach(([a, ba], i) => boxes.slice(i + 1).forEach(([b, bb]) => overlaps(ba, bb) && out.push([a, b])));
		return out;
	}

	let poolOpen = $state(false);
	let pool = $state<{ i: number | null; name: string; map: string; parks: string[] }>({ i: null, name: '', map: '', parks: [] });
	function editPool(i: number | null) {
		const p = i === null ? null : info!.pools[i]!;
		const map = p?.map ?? (currentMap && poolMaps.some((m) => same(m, currentMap)) ? currentMap : (poolMaps[0] ?? ''));
		// A park taken out of the setup since (from the console, say) can't stay in it.
		pool = { i, name: p?.name ?? '', map, parks: (p?.parks ?? []).filter((n) => setup.some((s) => s.name === n)) };
		poolOpen = true;
	}
	const choices = $derived(setup.filter((p) => same(p.map, pool.map)));
	const chosenObjects = $derived(
		pool.parks.reduce((n, name) => {
			const p = setup.find((s) => s.name === name);
			return n + ((p && fileFor(p)?.objects) || 0);
		}, 0)
	);
	const poolClashes = $derived(clashes(pool.parks));
	const poolProblem = $derived.by(() => {
		const name = pool.name.trim();
		if (!name) return 'Name it.';
		if ((info?.pools ?? []).some((p, i) => i !== pool.i && same(p.name, name))) return `A pool is called ${name} already.`;
		if (!pool.parks.length) return 'Pick its parks.';
		if (info && pool.parks.length > info.limits.parks) return `The server spawns at most ${info.limits.parks} parks at once.`;
		return '';
	});
	async function savePool() {
		if (!info || poolProblem) return;
		const next = { name: pool.name.trim(), map: pool.map, parks: pool.parks };
		const pools = pool.i === null ? [...info.pools, next] : info.pools.map((p, i) => (i === pool.i ? next : p));
		if (await saveParks(pools, info.chat)) {
			poolOpen = false;
			toast.ok(`Saved the pool ${next.name}`);
		}
	}
	async function deletePool(i: number) {
		const p = info!.pools[i]!;
		if (!(await ask({ title: `Delete the pool ${p.name}?`, body: `Its parks stay set up; it just stops going up as ${p.map} loads.`, action: 'Delete' }))) return;
		const pools = info!.pools.filter((_, j) => j !== i);
		if (await saveParks(pools, info!.chat)) toast.ok(`Deleted the pool ${p.name}`);
	}

	// The pool editor's map of its parks from above, as their boxes lie.
	const footprint = $derived.by(() => {
		const parks = choices.flatMap((p) => {
			const f = fileFor(p);
			return f?.objects ? [{ name: p.name, box: f.box, on: pool.parks.includes(p.name) }] : [];
		});
		if (!parks.length) return null;
		const [ax, ay] = groundAxes(parks.map((p) => p.box));
		const lo = (i: number) => Math.min(...parks.map((p) => p.box[i]!));
		const hi = (i: number) => Math.max(...parks.map((p) => p.box[i + 3]!));
		const margin = Math.max(hi(ax) - lo(ax), hi(ay) - lo(ay), 20) * 0.08;
		const view = [lo(ax) - margin, lo(ay) - margin, hi(ax) - lo(ax) + 2 * margin, hi(ay) - lo(ay) + 2 * margin];
		const clash = new Set(poolClashes.flat());
		return {
			view: view.join(' '),
			label: Math.max(view[2]!, view[3]!) / 22,
			rects: parks.map((p) => ({
				...p,
				clash: clash.has(p.name),
				x: p.box[ax]!,
				y: p.box[ay]!,
				w: Math.max(p.box[ax + 3]! - p.box[ax]!, margin / 3),
				h: Math.max(p.box[ay + 3]! - p.box[ay]!, margin / 3)
			}))
		};
	});

	// /parks: nobody, some roles or everyone, and for how long. The form takes
	// the saved choice afresh only when that changes, so a reload every half
	// minute keeps what's being edited.
	type Who = 'nobody' | 'roles' | 'everyone';
	const whoChoices: [Who, string][] = [
		['nobody', 'Nobody'],
		['roles', 'Some roles'],
		['everyone', 'Everyone']
	];
	const whoOf = (c: ParkChat): Who => (c.everyone ? 'everyone' : c.roles.length ? 'roles' : 'nobody');
	let who = $state<Who>('nobody');
	let roles = $state<string[]>([]);
	let minutes = $state(30);
	let seenChat = '';
	$effect(() => {
		const saved = JSON.stringify(info?.chat ?? null);
		if (!info || saved === seenChat) return;
		seenChat = saved;
		[who, roles, minutes] = [whoOf(info.chat), [...info.chat.roles], info.chat.minutes];
	});
	const chatForm = $derived<ParkChat>({ everyone: who === 'everyone', roles: who === 'roles' ? roles : [], minutes });
	const chatDirty = $derived(!!info && JSON.stringify(chatForm) !== JSON.stringify(info.chat));
	const chatOk = $derived(Number.isInteger(minutes) && minutes >= 0 && minutes <= 1440 && (who !== 'roles' || roles.length > 0));
	const toggleRole = (r: string) => (roles = roles.includes(r) ? roles.filter((x) => x !== r) : [...roles, r]);
	let savingChat = $state(false);
	async function saveChat() {
		if (!info) return;
		savingChat = true;
		if (await saveParks(info.pools, $state.snapshot(chatForm))) toast.ok('Saved who may use /parks');
		savingChat = false;
	}
	const chatOn = $derived(!!info && whoOf(info.chat) !== 'nobody');

	// ---- the mod itself ----

	const modPath = (mod: Mod) => `/instances/${ctx.id}/mods/${encodeURIComponent(mod.folder)}`;
	async function setEnabled(mod: Mod, on: boolean) {
		working = mod.folder;
		await attempt(() => api.patch(modPath(mod), { enabled: on }), `${on ? 'Enabled' : 'Disabled'} ${mod.title}`);
		working = '';
		load();
	}
	async function remove(mod: Mod) {
		const used = setup.filter((p) => p.folder === mod.folder);
		const ok = await ask({
			title: `Delete ${mod.title}?`,
			body: `This deletes the ${mod.folder} folder from the server for good.${used.length ? ` ${used.map((p) => p.name).join(', ')} ${used.length === 1 ? 'stays set up' : 'stay set up'}, and can't spawn until a mod with ${used.length === 1 ? 'its' : 'their'} park is back.` : ''}`,
			action: 'Delete'
		});
		if (!ok) return;
		working = mod.folder;
		await attempt(() => api.del(modPath(mod)), `Deleted ${mod.title}`);
		working = '';
		load();
	}
</script>

{#snippet upSticker(name: string)}
	{@const s = spawned.get(name)}
	{#if s}
		<span class="sticker sticker-accent" title="{plural(s.objects, 'object')} up">Up · {s.minutes ? `${s.minutes} min left` : 'until removed'}</span>
	{/if}
{/snippet}

{#snippet spawnButtons(p: ParkMod)}
	{#if canEdit && running}
		{#if spawned.has(p.name)}
			<button class="btn btn-sm" disabled={!!working} onclick={() => takeDown([p.name])}><Icon name="x" size={12} /> Take down</button>
		{:else}
			<button
				class="btn btn-primary btn-sm"
				disabled={!!working || !spawnable(p)}
				title={spawnable(p) ? `Spawn it ${durations.find(([m]) => m === spawnFor)?.[1]}` : `It spawns on ${p.map}, and the server is on ${currentMap}`}
				onclick={() => spawn([p.name])}><Icon name="play" size={12} /> Spawn</button
			>
		{/if}
	{/if}
{/snippet}

<ModDropZone label="Parks" hint="Drop .zip files of park mods to add them" enabled={canEdit} onfiles={(files) => mods.upload(files)}>
	<div class="flex flex-wrap items-end gap-3">
		<div class="w-full min-w-0 sm:w-auto sm:flex-1">
			<h1 class="page-title">Parks</h1>
			<p class="mt-2 max-w-prose text-sm text-muted">
				Parks built in the game's Park Editor, saved as mods. The server spawns them for everyone; players need no mod for them. {#if canEdit}Upload a park
					mod's zip (or drop it here), set its park up with a name, then spawn it.{/if}
			</p>
		</div>
		<ModTools
			browse="/browse?to={encodeURIComponent(ctx.id)}"
			busy={mods.busy}
			canUpload={canEdit}
			uploading={mods.uploading}
			noun="park"
			onrefresh={() => {
				mods.load(true);
				loadInfo();
			}}
			onfiles={(files) => mods.upload(files)}
		/>
	</div>

	{#if tooOld}
		<p class="flex items-start gap-2 rounded-xl border-2 border-dashed border-warn/60 bg-warn/5 px-4 py-3 text-sm text-muted">
			<Icon name="alert" size={16} class="mt-0.5 text-warn" />
			<span>Park mods need ReSkate 2.0.6, and this server runs {serverVersion}. Update it under <a class="text-accent hover:underline" href="/s/{ctx.id}/updates">Updates</a>.</span>
		</p>
	{/if}
	{#if mods.error || error}<LoadError error={mods.error || error} onretry={load} />{/if}

	{#if info && mods.list}
		<!-- What's up right now, and the server's limits. -->
		<section class="card flex flex-wrap items-center gap-x-5 gap-y-3 px-4 py-3">
			{#if running}
				<div class="min-w-0">
					<div class="text-xs text-faint">On {currentMap}</div>
					<div class="font-semibold tabular-nums">
						{info.spawned ? `${spawned.size} of ${info.limits.parks} parks up` : "Can't tell what's up"}
						<span class="font-normal text-muted"> · {upObjects} of {info.limits.objects} objects</span>
					</div>
				</div>
				<div class="flex h-2 min-w-24 flex-1 basis-32 overflow-hidden rounded-full border-2 border-ink bg-sunken" role="img" aria-label="{spawned.size} of {info.limits.parks} parks up">
					<div class="bg-accent transition-[width] duration-300" style:width="{Math.min(100, (spawned.size / Math.max(1, info.limits.parks)) * 100)}%"></div>
				</div>
				{#if canEdit}
					<label class="flex items-center gap-2 text-sm text-muted">
						Spawns last
						<select class="input h-7 w-auto py-0 text-xs" bind:value={spawnFor}>
							{#each durations as [m, label] (m)}<option value={m}>{label}</option>{/each}
						</select>
					</label>
					{#if spawned.size}
						<button class="btn btn-sm" disabled={!!working} onclick={() => takeDown([...spawned.keys()])}><Icon name="x" size={12} /> Take all down</button>
					{/if}
				{/if}
			{:else}
				<p class="min-w-0 flex-1 basis-64 text-sm text-muted">
					The server is {serverState ?? 'stopped'}. Parks spawn while it runs, and a pool goes up as each map loads.
				</p>
			{/if}
			<button class="btn btn-ghost btn-sm ml-auto" onclick={editLimits} title="At most {info.limits.parks} parks and {info.limits.objects} objects at once">
				<Icon name="sliders" size={12} /> Limits
			</button>
			{#if running && spawned.size}
				<ul class="flex w-full flex-wrap gap-1.5" aria-label="Parks up">
					{#each [...spawned.values()] as sp (sp.name)}
						<li class="inline-flex items-center rounded-md border-2 border-ink bg-accent/15 font-mono text-xs leading-6">
							<span class="pl-2 {canEdit ? '' : 'pr-2'}">{sp.name}{#if sp.minutes}<span class="text-muted"> · {sp.minutes} min</span>{/if}</span>
							{#if canEdit}
								<button
									class="grid size-6 place-items-center rounded-r text-muted transition-colors hover:bg-bad/20 hover:text-bad disabled:opacity-50"
									disabled={!!working}
									title="Take it down"
									aria-label="Take {sp.name} down"
									onclick={() => takeDown([sp.name])}><Icon name="x" size={12} /></button
								>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</section>

		<div class="grid items-start gap-4 sm:gap-6 xl:grid-cols-[minmax(0,1fr)_21rem]">
			<div class="min-w-0 space-y-4 sm:space-y-6">
				{#if !parkMods.length}
					<div class="card grid place-items-center gap-3 px-6 py-10 text-center">
						<Icon name="ramp" size={36} class="text-accent" />
						<h2>No park mods yet</h2>
						<p class="max-w-md text-sm text-muted">
							A park mod is a folder with <code>parks/bam.park.json</code> in it, saved from the game's Park Editor into its Mods folder. Zip that folder and upload it
							here.
						</p>
					</div>
				{/if}

				{#each sortedMods as mod (mod.folder)}
					{@const updating = uploads.pending(ctx.id, mod.folder)}
					<section class="card">
						<div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b-2 border-line px-4 py-3">
							<div class="flex min-w-0 flex-1 basis-48 flex-wrap items-baseline gap-x-3 gap-y-1">
								<h2 class="min-w-0 break-words {mod.disabled ? 'text-muted' : ''}">{mod.title}</h2>
								<ModCredits {mod} up={mods.updates[mod.folder]}>
									{#if mod.shared}<span class="sticker sticker-info" title="Linked from the shared mods, which update and delete it">Shared</span>{/if}
									{#if mod.disabled}<span class="sticker">Disabled</span>{/if}
								</ModCredits>
							</div>
							<div class="flex shrink-0 flex-wrap items-center gap-1.5">
								<ModUpdate {mod} up={mods.updates[mod.folder]} can={canEdit} {updating} onupdate={() => mods.update(mod)} elsewhere={mod.shared ? '/shared-mods' : undefined} />
								{#if !mod.shared && !mod.disabled}
									<a class="btn btn-sm" href="/s/{ctx.id}/parks/{encodeURIComponent(mod.folder)}"><Icon name="pencil" size={12} /> {canEdit ? 'Edit files' : 'Files'}</a>
								{/if}
								{#if canEdit}
									<button class="btn btn-sm" disabled={working === mod.folder || updating} onclick={() => setEnabled(mod, !!mod.disabled)}>
										<Icon name="power" size={12} />
										{mod.disabled ? 'Enable' : 'Disable'}
									</button>
									{#if mod.shared}
										<a class="btn btn-ghost btn-sm size-7 px-0" href="/shared-mods" title="Manage under Shared mods" aria-label="Manage {mod.title} under Shared mods"
											><Icon name="package" size={13} /></a
										>
									{:else}
										<button
											class="btn btn-danger btn-sm size-7 px-0"
											disabled={working === mod.folder || updating}
											onclick={() => remove(mod)}
											title="Delete"
											aria-label="Delete {mod.title}"><Icon name="trash" size={13} /></button
										>
									{/if}
								{/if}
							</div>
						</div>
						{#if mod.description || mod.title !== mod.folder}
							<div class="space-y-1 border-b-2 border-line px-4 py-3">
								{#if mod.title !== mod.folder}<p class="font-mono text-xs break-all text-faint">{mod.folder}</p>{/if}
								{#if mod.description}<p class="text-sm text-muted">{mod.description}</p>{/if}
							</div>
						{/if}
						<ul class="divide-y-2 divide-line {mod.disabled ? 'opacity-60' : ''}">
							{#each mod.parks ?? [] as key (key)}
								{@const file = fileOf(mod.folder, key)}
								{@const here = setupsOf(mod, key)}
								{@const id = rowId(mod, key)}
								{@const name = draftName(mod, key)}
								{@const problem = nameProblem(name)}
								<li>
									<div class="flex flex-wrap items-center gap-x-3 gap-y-1 bg-sunken/40 px-4 py-2">
										<span class="rounded-md border-2 border-ink bg-raised px-1.5 font-mono text-xs leading-5" title="parks/{key}.park.json">{key}</span>
										<span class="text-sm font-medium">{keyMaps[lower(key)] ?? 'A custom base map'}</span>
										{#if file && !file.problem}<span class="text-xs text-faint tabular-nums">{plural(file.objects, 'object')}</span>{/if}
										{#if file?.skipped}
											<span class="text-xs text-warn tabular-nums" title="Not Build Kit items, or broken: players' games can't show them, so the server leaves them out"
												>{plural(file.skipped, 'object')} left out</span
											>
										{/if}
										{#if mod.disabled}<span class="text-xs text-faint">Enable the mod to spawn it.</span>{/if}
									</div>
									{#if file?.problem}
										<p class="flex items-start gap-1.5 px-4 py-2 text-sm text-bad">
											<Icon name="alert" size={14} class="mt-0.5" /><span class="min-w-0 break-words">The server won't spawn this park: {file.problem}. Fix it under Edit files.</span>
										</p>
									{/if}
									{#each here as p (p.name)}
										<div class="flex items-center gap-3 border-t border-line/60 px-4 py-2">
											<!-- The buttons keep their place; what's said of the park wraps under its name. -->
											<div class="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
												<span class="font-mono text-sm font-semibold">{p.name}</span>
												<!-- Its group's map goes without saying; a custom map that shares it doesn't. -->
												{#if !same(p.map, keyMaps[lower(key)] ?? '')}<span class="text-xs text-muted">on {p.map}</span>{/if}
												{@render upSticker(p.name)}
											</div>
											<div class="flex shrink-0 items-center gap-1.5">
												{@render spawnButtons(p)}
												{#if canEdit}
													<button
														class="btn btn-ghost btn-sm size-7 px-0"
														disabled={!!working}
														title="Take it out of the setup"
														aria-label="Take {p.name} out"
														onclick={() => takeOut(p)}><Icon name="trash" size={13} /></button
													>
												{/if}
											</div>
										</div>
									{/each}
									{#if canEdit && !mod.disabled && !file?.problem}
										{#if here.length && !drafts[id]?.open}
											<div class="border-t border-line/60 px-3 py-1.5">
												<button class="btn btn-ghost btn-sm" onclick={() => (drafts[id] = { ...drafts[id], open: true })}><Icon name="plus" size={12} /> Set it up on another map</button>
											</div>
										{:else}
											<form
												class="flex flex-wrap items-end gap-2 border-t border-line/60 px-4 py-2.5"
												onsubmit={(e) => {
													e.preventDefault();
													if (!problem) setUp(mod, key);
												}}
											>
												<label class="block w-36">
													<span class="label">Name</span>
													<input
														class="input h-7 font-mono"
														maxlength="16"
														spellcheck="false"
														autocomplete="off"
														aria-invalid={!!problem}
														value={name}
														oninput={(e) => (drafts[id] = { ...drafts[id], name: e.currentTarget.value.toLowerCase() })}
													/>
												</label>
												<label class="block min-w-40 flex-1">
													<span class="label">Spawns on</span>
													<select class="input h-7 py-0" value={draftMap(key, mod)} onchange={(e) => (drafts[id] = { ...drafts[id], map: e.currentTarget.value })}>
														{#each [...new Set([draftMap(key, mod), ...mapNames])] as m (m)}<option value={m}>{m}</option>{/each}
													</select>
												</label>
												<button class="btn btn-primary btn-sm" disabled={!!problem || working === id}><Icon name="check" size={12} /> Set up</button>
												{#if problem}<p class="w-full text-xs text-bad">{problem}</p>{/if}
											</form>
										{/if}
									{/if}
								</li>
							{/each}
						</ul>
					</section>
				{/each}

				{#if missing.length}
					<section class="card border-dashed">
						<h2 class="flex items-center gap-1.5 border-b-2 border-line px-4 py-3"><Icon name="alert" size={16} class="text-warn" /> Set up, but not in Mods</h2>
						<p class="px-4 pt-3 text-sm text-muted">The server can't spawn these: their mod isn't in its Mods folder, is disabled, or has no such park.</p>
						<ul class="divide-y divide-line/60 px-1 py-1.5">
							{#each missing as p (p.name)}
								<li class="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
									<span class="font-mono text-sm font-semibold">{p.name}</span>
									<span class="min-w-0 flex-1 truncate text-xs text-muted" title={p.folder}>{p.folder}{p.key ? ` (${p.key})` : ''} on {p.map}</span>
									{#if canEdit}
										<button class="btn btn-ghost btn-sm" disabled={!!working} onclick={() => takeOut(p)}><Icon name="trash" size={12} /> Take out</button>
									{/if}
								</li>
							{/each}
						</ul>
					</section>
				{/if}
			</div>

			<div class="min-w-0 space-y-4 sm:space-y-6">
				<section class="card">
					<h2 class="flex items-center gap-2 border-b-2 border-line px-4 py-3">
						Pools
						{#if canEdit && setup.length}
							<button class="btn btn-sm -my-0.5 ml-auto" onclick={() => editPool(null)}><Icon name="plus" size={12} /> New pool</button>
						{/if}
					</h2>
					<p class="px-4 pt-3 text-xs text-faint">As a map loads, one of its pools goes up at random and stays until taken down. Put parks that fit together in one.</p>
					{#if !info.pools.length}
						<p class="px-4 py-3 text-sm text-muted">
							{setup.length ? 'No pools yet: nothing goes up by itself.' : 'Set up a park first, then group parks into pools.'}
						</p>
					{/if}
					{#each poolsByMap as [m, list] (m)}
						<div class="px-4 pt-4 pb-2">
							<h3 class="flex items-center gap-2 text-[11px] font-semibold tracking-wider text-muted uppercase">
								{m}
								{#if currentMap && same(m, currentMap)}<span class="sticker sticker-accent normal-case">Current map</span>{/if}
							</h3>
						</div>
						<ul class="divide-y-2 divide-line border-t-2 border-line">
							{#each list as p (p.i)}
								{@const clash = clashes(p.parks)}
								<li class="px-4 py-2.5">
									<div class="flex items-center gap-2">
										<span class="min-w-0 flex-1 truncate font-medium">{p.name}</span>
										{#if canEdit}
											{#if running && same(p.map, currentMap)}
												<button class="btn btn-ghost btn-sm size-7 px-0" disabled={!!working} title="Spawn it now" aria-label="Spawn {p.name} now" onclick={() => spawn(p.parks, 0)}
													><Icon name="play" size={12} /></button
												>
											{/if}
											<button class="btn btn-ghost btn-sm size-7 px-0" title="Edit" aria-label="Edit {p.name}" onclick={() => editPool(p.i)}><Icon name="pencil" size={12} /></button>
											<button class="btn btn-ghost btn-sm size-7 px-0" title="Delete" aria-label="Delete {p.name}" onclick={() => deletePool(p.i)}><Icon name="trash" size={12} /></button>
										{/if}
									</div>
									<div class="mt-1.5 flex flex-wrap gap-1">
										{#each p.parks as n (n)}
											{@const gone = !setup.some((s) => s.name === n)}
											<span
												class="rounded-md bg-raised px-1.5 font-mono text-xs leading-5 {gone ? 'text-faint line-through' : spawned.has(n) ? 'text-accent' : 'text-muted'}"
												title={gone ? 'Not set up any more: it spawns without it' : spawned.has(n) ? 'Up' : undefined}>{n}</span
											>
										{/each}
									</div>
									{#if clash.length}
										<p class="mt-1.5 flex items-start gap-1.5 text-xs text-warn">
											<Icon name="alert" size={12} class="mt-0.5" />
											{clash.map(([a, b]) => `${a} and ${b}`).join(', ')} may overlap.
										</p>
									{/if}
								</li>
							{/each}
						</ul>
					{/each}
				</section>

				<section class="card">
					<h2 class="border-b-2 border-line px-4 py-3">Players</h2>
					<div class="space-y-3 p-4">
						<p class="text-sm text-muted">
							Who may spawn and take down parks from chat: <code class="whitespace-nowrap">/parks</code> lists this map's,
							<code class="whitespace-nowrap">/parks add street</code>, <code class="whitespace-nowrap">/parks remove street</code>. A role that has
							park-mod always may.
						</p>
						<div class="seg" role="group" aria-label="Who may use /parks">
							{#each whoChoices as [w, label] (w)}
								<button class={who === w ? 'seg-on' : ''} aria-pressed={who === w} disabled={!canEdit} onclick={() => (who = w)}>{label}</button>
							{/each}
						</div>
						{#if who === 'roles'}
							{#if info.roles.length}
								<div class="flex flex-wrap gap-1.5" role="group" aria-label="Roles that may use /parks">
									{#each info.roles as r (r)}
										<button class="btn btn-sm {roles.includes(r) ? 'btn-primary' : ''}" aria-pressed={roles.includes(r)} disabled={!canEdit} onclick={() => toggleRole(r)}
											>{#if roles.includes(r)}<Icon name="check" size={12} />{/if}{r}</button
										>
									{/each}
								</div>
								{#if !roles.length}<p class="text-xs text-faint">Tick the roles that may.</p>{/if}
							{:else}
								<p class="text-sm text-muted">The server has no roles yet: it writes them as it first starts.</p>
							{/if}
						{/if}
						{#if who !== 'nobody'}
							<label class="flex flex-wrap items-center gap-2 text-sm text-muted">
								Their parks go after
								<input class="input w-20 tabular-nums" type="number" min="0" max="1440" disabled={!canEdit} bind:value={minutes} />
								minutes
								<span class="w-full text-xs text-faint">0 keeps them up until someone takes them down.</span>
							</label>
						{/if}
						{#if chatOn && info.command === 'taken'}
							<p class="flex items-start gap-1.5 text-xs text-warn">
								<Icon name="alert" size={12} class="mt-0.5" /><span>A chat command of your own is called /parks, and it comes first. Rename it under <a class="underline" href="/s/{ctx.id}/settings#chat">Settings</a>.</span>
							</p>
						{:else if chatOn && running && info.command !== 'manager'}
							<p class="flex items-start gap-1.5 text-xs text-muted">
								<Icon name="info" size={12} class="mt-0.5" /><span>The server reads its chat commands as it starts: /parks works after its next restart.</span>
							</p>
						{/if}
						{#if canEdit}
							<button class="btn btn-primary" disabled={!chatDirty || !chatOk || savingChat} onclick={saveChat}><Icon name="check" size={14} /> Save</button>
						{/if}
					</div>
				</section>
			</div>
		</div>
	{:else if !mods.error && !error}
		<Loading />
	{/if}
</ModDropZone>

<Modal bind:open={poolOpen} title={pool.i === null ? 'New pool' : `Edit ${info?.pools[pool.i]?.name ?? 'pool'}`} wide>
	<form
		id="pool-form"
		class="space-y-4"
		onsubmit={(e) => {
			e.preventDefault();
			savePool();
		}}
	>
		<div class="flex flex-wrap gap-3">
			<label class="block min-w-40 flex-1">
				<span class="label">Name</span>
				<input class="input" maxlength="32" placeholder="Street set" bind:value={pool.name} />
			</label>
			<label class="block min-w-40 flex-1">
				<span class="label">Map</span>
				<select class="input" bind:value={pool.map} onchange={() => (pool.parks = [])}>
					{#each poolMaps as m (m)}<option value={m}>{m}</option>{/each}
				</select>
			</label>
		</div>
		<fieldset>
			<legend class="label">Parks</legend>
			{#if choices.length}
				<div class="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
					<ul class="max-h-56 space-y-1 overflow-y-auto">
						{#each choices as p (p.name)}
							{@const f = fileFor(p)}
							{@const on = pool.parks.includes(p.name)}
							<li>
								<label class="flex cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 hover:bg-raised">
									<input
										type="checkbox"
										class="size-4 accent-accent"
										checked={on}
										disabled={!on && !!info && pool.parks.length >= info.limits.parks}
										onchange={(e) => (pool.parks = e.currentTarget.checked ? [...pool.parks, p.name] : pool.parks.filter((n) => n !== p.name))}
									/>
									<span class="font-mono text-sm">{p.name}</span>
									<span class="ml-auto text-xs text-faint tabular-nums">{f ? plural(f.objects, 'object') : 'not in Mods'}</span>
								</label>
							</li>
						{/each}
					</ul>
					{#if footprint}
						<figure class="rounded-xl border-2 border-line bg-sunken p-2">
							<svg viewBox={footprint.view} class="h-44 w-full" role="img" aria-label="The parks from above, where their objects lie">
								{#each footprint.rects as r (r.name)}
									<rect
										x={r.x}
										y={r.y}
										width={r.w}
										height={r.h}
										rx={footprint.label / 3}
										vector-effect="non-scaling-stroke"
										stroke-width="2"
										class={r.clash ? 'fill-bad/25 stroke-bad' : r.on ? 'fill-accent/25 stroke-accent' : 'fill-transparent stroke-line-strong [stroke-dasharray:4_4]'}
									/>
								{/each}
								<!-- Over every box, so one box's edge never crosses another's name. -->
								{#each footprint.rects as r (r.name)}
									<text
										x={r.x + r.w / 2}
										y={r.y + r.h / 2}
										font-size={footprint.label}
										stroke-width={footprint.label / 4}
										text-anchor="middle"
										dominant-baseline="middle"
										paint-order="stroke"
										class="stroke-sunken font-mono {r.on ? 'fill-fg' : 'fill-faint'}">{r.name}</text
									>
								{/each}
							</svg>
							<figcaption class="mt-1 text-[11px] text-faint">From above. Dashed: not in this pool.</figcaption>
						</figure>
					{/if}
				</div>
			{:else}
				<p class="text-sm text-muted">No parks are set up for {pool.map || 'this map'}.</p>
			{/if}
		</fieldset>
		{#if info}
			<p class="text-sm text-muted tabular-nums">
				{pool.parks.length} of {info.limits.parks} parks · {chosenObjects} of {info.limits.objects} objects
			</p>
			{#if chosenObjects > info.limits.objects}
				<p class="flex items-start gap-1.5 text-sm text-warn">
					<Icon name="alert" size={14} class="mt-0.5" /> That's more objects than the server spawns at once, so it would refuse the pool.
				</p>
			{/if}
		{/if}
		{#if poolClashes.length}
			<p class="flex items-start gap-1.5 text-sm text-warn">
				<Icon name="alert" size={14} class="mt-0.5" />
				<span>{poolClashes.map(([a, b]) => `${a} and ${b}`).join(', ')} may overlap: their objects come within a few metres of each other.</span>
			</p>
		{/if}
	</form>
	{#snippet actions()}
		{#if poolProblem && pool.name.trim()}<span class="mr-auto self-center text-xs text-faint">{poolProblem}</span>{/if}
		<button class="btn" onclick={() => (poolOpen = false)}>Cancel</button>
		<button class="btn btn-primary" form="pool-form" disabled={!!poolProblem}><Icon name="check" size={14} /> Save pool</button>
	{/snippet}
</Modal>

<Modal bind:open={limitsOpen} title="Park limits">
	<form
		id="limits-form"
		class="space-y-3"
		onsubmit={(e) => {
			e.preventDefault();
			if (limitsOk) saveLimits();
		}}
	>
		<p class="text-sm text-muted">How much the server spawns at once, pools and players' parks together. It reads these as it starts.</p>
		<div class="flex gap-3">
			<label class="block flex-1">
				<span class="label">Parks, 1 to 16</span>
				<input class="input tabular-nums" type="number" min="1" max="16" bind:value={limits.parks} />
			</label>
			<label class="block flex-1">
				<span class="label">Objects, 1 to 1024</span>
				<input class="input tabular-nums" type="number" min="1" max="1024" bind:value={limits.objects} />
			</label>
		</div>
	</form>
	{#snippet actions()}
		<button class="btn" onclick={() => (limitsOpen = false)}>Cancel</button>
		{#if canEdit}<button class="btn btn-primary" form="limits-form" disabled={!limitsOk}><Icon name="check" size={14} /> Save</button>{/if}
	{/snippet}
</Modal>
