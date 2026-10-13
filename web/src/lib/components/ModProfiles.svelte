<script lang="ts">
	import { untrack } from 'svelte';
	import { api, message, upload, type ProfileEntry, type SharedProfile } from '#lib/api.js';
	import { attempt, toast } from '#lib/toast.svelte.js';
	import { copy } from '#lib/clipboard.js';
	import { thunderstore, type MapsLook } from '#lib/thunderstore.svelte.js';
	import { uploads } from '#lib/uploads.svelte.js';
	import { newest } from '#lib/newest.js';
	import { bytes, plural } from '#lib/format.js';
	import Icon from './Icon.svelte';
	import Modal from './Modal.svelte';

	// Launcher profiles (ReSkate 2.0.8): the maps a server loads, which players
	// import in the launcher to get them all at once, and mods to add from a
	// profile someone made. changed is anything that moves when the mods do.
	let { id, server, canAdd, changed }: { id: string; server: string; canAdd: boolean; changed: unknown } = $props();

	const base = $derived(`/instances/${id}/mods/profile`);
	let profile = $state.raw<SharedProfile | null>(null);
	let error = $state('');
	const begin = newest();
	async function load() {
		const current = begin();
		try {
			const p = await api.get<SharedProfile>(base);
			if (!current()) return;
			profile = p;
			error = '';
		} catch (e) {
			if (current()) error = message(e);
		}
	}
	$effect(() => {
		changed;
		untrack(load);
	});

	// A code stands for the list it was made from.
	const listed = $derived(JSON.stringify(profile?.mods ?? []));
	let code = $state<{ code: string; for: string } | null>(null);
	const shownCode = $derived(code?.for === listed ? code.code : '');
	let coding = $state(false);
	async function getCode() {
		coding = true;
		const made = listed;
		const r = await attempt(() => api.post<{ code: string }>(`${base}/code`));
		coding = false;
		if (r) code = { code: r.code, for: made };
	}
	const behind = $derived(profile?.mods.filter((m) => m.server) ?? []);

	// ---- Adding mods from a profile ----

	let addOpen = $state(false);
	let codeIn = $state('');
	let reading = $state(false);
	let read = $state.raw<{ name: string; mods: ProfileEntry[] } | null>(null);
	let touched = $state<Record<number, boolean>>({});
	let picker = $state<HTMLInputElement>();

	function openAdd() {
		read = null;
		codeIn = '';
		touched = {};
		addOpen = true;
	}

	async function readWith(fn: () => Promise<{ name: string; mods: ProfileEntry[] }>) {
		reading = true;
		const r = await attempt(fn);
		reading = false;
		if (!r) return;
		read = r;
		touched = {};
		for (const e of r.mods) if (installable(e) && !e.maps && !e.unknown) thunderstore.lookAt(e.package!, e.install!);
	}
	function readCode(e: SubmitEvent) {
		e.preventDefault();
		if (codeIn.trim()) readWith(() => api.post(`${base}/read`, { code: codeIn.trim() }));
	}
	function readFile(files: FileList | null) {
		const f = files?.[0];
		if (f) readWith(() => upload('POST', `${base}/read`, f));
	}

	const installable = (e: ProfileEntry) => !!e.package && (e.state === 'new' || e.state === 'replaces');
	function look(e: ProfileEntry): MapsLook | undefined {
		if (e.maps) return { state: 'done', maps: e.maps };
		if (e.unknown) return { state: 'unknown', reason: e.unknown };
		return e.package && e.install ? thunderstore.mapsOf(e.package, e.install) : undefined;
	}
	// Ticked: what was picked by hand, else each mod the profile has on that adds a map.
	function ticked(e: ProfileEntry, i: number) {
		if (!installable(e)) return false;
		const l = look(e);
		return touched[i] ?? (e.enabled && l?.state === 'done' && l.maps.length > 0);
	}
	const chosen = $derived(read?.mods.filter(ticked) ?? []);
	const looking = $derived(read?.mods.filter((e) => installable(e) && look(e)?.state === 'looking').length ?? 0);

	function install() {
		for (const e of chosen) uploads.install(id, server, { fullName: e.package!, title: e.title || e.name }, e.install);
		toast.ok(`Installing ${plural(chosen.length, 'mod')}, one at a time`);
		addOpen = false;
	}
</script>

<section class="card p-4">
	<div class="flex flex-wrap items-start gap-x-4 gap-y-3">
		<div class="min-w-0 flex-1 basis-72">
			<div class="font-semibold">Launcher profile</div>
			<p class="text-sm text-muted">
				Players import it in the ReSkate launcher (2.0.8 or later, under Mods, Profiles) to download every map this server loads, at its version. r2modman reads it
				too.
			</p>
		</div>
		<div class="flex flex-wrap gap-2">
			<button class="btn" disabled={!profile?.mods.length || coding} onclick={getCode} title="Put the profile on Thunderstore and show its code"
				><Icon name="send" size={14} /> Get a code</button
			>
			{#if profile?.mods.length}
				<a class="btn" href="/api{base}.r2z" download><Icon name="download" size={14} /> Download file</a>
			{/if}
			{#if canAdd}
				<button class="btn" onclick={openAdd} title="Install the mods of a profile from the launcher or r2modman"><Icon name="plus" size={14} /> Add from a profile</button>
			{/if}
		</div>
	</div>

	{#if shownCode}
		<div class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg border-2 border-line px-3 py-2">
			<code class="font-mono text-base select-all">{shownCode}</code>
			<button class="btn btn-sm" onclick={() => copy(shownCode)}><Icon name="copy" size={12} /> Copy</button>
			<span class="basis-full text-xs text-faint">Thunderstore keeps codes for a limited time. Get a new one when it stops working, or share the file, which doesn't expire.</span>
		</div>
	{/if}

	{#if profile}
		<div class="mt-3 space-y-1 text-sm text-muted">
			{#if profile.mods.length}
				<p>In it: {profile.mods.map((m) => m.title).join(', ')}.</p>
			{:else}
				<p>The server loads no maps from mods on Thunderstore yet, so there's nothing to share.</p>
			{/if}
			{#each behind as m (m.package)}
				<p class="flex items-start gap-1.5"><Icon name="info" size={14} class="mt-0.5" /> {m.title}: Thunderstore no longer has the server's v{m.server}, so players get v{m.version}.</p>
			{/each}
			{#if profile.left.length}
				<p class="flex items-start gap-1.5 text-warn">
					<Icon name="alert" size={14} class="mt-0.5" /> Not in it: {profile.left.map((l) => `${l.title} (${l.why})`).join(', ')}. Players install those by hand.
				</p>
			{/if}
		</div>
	{:else if error}
		<p class="mt-3 text-sm text-faint">Can't tell what's in it: {error}</p>
	{/if}
</section>

<Modal bind:open={addOpen} title={read ? `Add mods from ${read.name}` : 'Add mods from a profile'} wide>
	<div class="space-y-4 text-sm">
		{#if !read}
			<p class="text-muted">
				A profile from the ReSkate launcher (2.0.8 or later) or r2modman: its code, or its .r2z file. You pick which of its mods to install here; the server only uses
				the ones that add maps.
			</p>
			<form class="flex flex-wrap gap-2" onsubmit={readCode}>
				<input class="input min-w-0 flex-1 basis-56 font-mono" placeholder="Profile code" aria-label="Profile code" bind:value={codeIn} autocomplete="off" spellcheck="false" />
				<button class="btn btn-primary" disabled={reading || !codeIn.trim()}>Read</button>
			</form>
			<div class="flex flex-wrap items-center gap-2">
				<span class="text-muted">or</span>
				<input
					bind:this={picker}
					type="file"
					accept=".r2z,.r2x,.zip"
					hidden
					onchange={(e) => {
						readFile(e.currentTarget.files);
						e.currentTarget.value = '';
					}}
				/>
				<button class="btn" disabled={reading} onclick={() => picker?.click()}><Icon name="upload" size={14} /> Choose a file</button>
			</div>
		{:else}
			<p class="text-muted">
				Ticked: the mods it has on that add maps. Tick a park mod you'll set up under Settings too; the rest only matter to players' games.{looking
					? ` Looking inside ${plural(looking, 'mod')}…`
					: ''}
			</p>
			{#if read.mods.length}
				<ul class="max-h-[50vh] divide-y divide-line/60 overflow-y-auto rounded-lg border-2 border-line">
					{#each read.mods as e, i (i)}
						{@const l = look(e)}
						<li class="flex items-start gap-3 px-3 py-2">
							<input
								type="checkbox"
								class="mt-1"
								checked={ticked(e, i)}
								disabled={!installable(e)}
								onchange={(ev) => (touched[i] = ev.currentTarget.checked)}
								aria-label="Install {e.title || e.name}"
							/>
							<div class="min-w-0 flex-1">
								<div class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
									<span class="font-medium {installable(e) ? '' : 'text-muted'}">{e.title || e.name}</span>
									<span class="text-xs text-faint tabular-nums">v{e.install || e.version}{e.size ? ` · ${bytes(e.size)}` : ''}</span>
									{#if !e.enabled}<span class="sticker">Off in the profile</span>{/if}
									{#if e.state === 'replaces'}<span class="sticker sticker-info">Replaces v{e.replaces}</span>{/if}
									{#if e.state === 'installed'}<span class="sticker sticker-accent" title={e.note}>Installed</span>{/if}
								</div>
								<div class="text-xs text-muted">
									{#if e.state === 'absent'}
										Not on Thunderstore
									{:else if e.state === 'installed'}
										{e.note}
									{:else if l?.state === 'looking'}
										Looking for maps…
									{:else if l?.state === 'done'}
										{l.maps.length ? `Adds ${l.maps.map((m) => m.name).join(', ')}` : 'No maps'}
									{:else if l?.state === 'unknown'}
										{l.reason}
									{:else if l?.state === 'failed'}
										Couldn't look inside it: {l.error}
									{/if}
									{#if e.package && e.install !== e.version}
										· v{e.version} isn't on Thunderstore, so it's the newest
									{/if}
								</div>
							</div>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="text-muted">The profile has no mods.</p>
			{/if}
		{/if}
	</div>
	{#snippet actions()}
		{#if read}<button class="btn mr-auto" onclick={() => (read = null)}>Another profile</button>{/if}
		<button class="btn" onclick={() => (addOpen = false)}>Cancel</button>
		{#if read}
			<button class="btn btn-primary" disabled={!chosen.length} onclick={install}><Icon name="download" size={14} /> Install {chosen.length || ''}</button>
		{/if}
	{/snippet}
</Modal>
