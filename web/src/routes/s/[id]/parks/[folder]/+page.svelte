<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError, message, upload, type ModList } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { ask } from '#lib/ask.svelte.js';
	import { toast } from '#lib/toast.svelte.js';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';
	import { keyMaps } from '#lib/parkmods.js';
	import CodeEditor from '#lib/components/CodeEditor.svelte';
	import Icon from '#lib/components/Icon.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';

	// A park mod's files as text: its parks, which the server spawns, and its
	// manifest.json. The manager checks a park as the server reads it before
	// saving, and the running server reads a park that's up again in place.
	interface Doc {
		text: string;
		saved: string;
		hash: string;
	}

	const ctx = useLive();
	const canEdit = $derived(ctx.can('settings.edit'));
	const folder = $derived(page.params.folder ?? '');
	const fileURL = (path: string) => `/instances/${ctx.id}/mods/${encodeURIComponent(folder)}/file?path=${encodeURIComponent(path)}`;

	let list = $state.raw<ModList | null>(null);
	let loadError = $state('');
	let docs = $state<Record<string, Doc>>({});
	let open = $state<string | null>(null);
	let saving = $state(false);

	const mod = $derived(list?.mods.find((m) => m.folder === folder));
	const files = $derived(mod ? [...(mod.parks ?? []).map((k) => `parks/${k}.park.json`), 'manifest.json'] : []);
	const changed = $derived(Object.entries(docs).flatMap(([p, d]) => (d.text !== d.saved ? [p] : [])));
	const doc = $derived(open ? docs[open] : undefined);
	// Not JSON at all: said as it's typed. The rest the manager says on saving.
	const invalid = $derived.by(() => {
		if (!doc) return '';
		try {
			JSON.parse(doc.text);
			return '';
		} catch (e) {
			// Where, short enough to sit beside Save; the editor marks the line too.
			const at = /line (\d+) column (\d+)/.exec(message(e));
			return at ? `line ${at[1]}, column ${at[2]}` : message(e);
		}
	});
	const blocked = $derived(changed.some((p) => {
		try {
			JSON.parse(docs[p]!.text);
			return false;
		} catch {
			return true;
		}
	}));

	guardUnsaved(() => changed.length > 0, 'Some files here aren’t saved yet; leaving drops your changes.');

	// Another mod's files, by the browser's back and forward, start afresh.
	$effect(() => {
		folder;
		untrack(() => {
			docs = {};
			open = null;
			list = null;
			load();
		});
	});

	async function load() {
		try {
			list = await api.get<ModList>(`/instances/${ctx.id}/mods`);
			loadError = '';
			if (!open && files[0]) await openFile(files[0]);
		} catch (e) {
			loadError = message(e);
		}
	}

	async function openFile(path: string) {
		if (!docs[path]) {
			try {
				const f = await api.get<{ text: string; hash: string }>(fileURL(path));
				docs[path] = { text: f.text, saved: f.text, hash: f.hash };
			} catch (e) {
				toast.error(e);
				return;
			}
		}
		open = path;
	}

	async function save() {
		if (saving || !changed.length || blocked) return;
		saving = true;
		for (const path of changed) {
			const reply = await write(path);
			if (reply === null) break;
			toast.ok(reply ? reply.split('\n')[0]!.replace(/^\[parks\] /, '') : `Saved ${path}`);
		}
		saving = false;
	}

	// Saves one file over the hash it was read with, or over hash once they
	// say so after it changed on disk. What the server said of it, or null.
	async function write(path: string, hash?: string, asked = false): Promise<string | null> {
		const d = docs[path];
		if (!d) return null;
		hash ??= d.hash;
		const text = d.text; // what's typed while it saves stays unsaved
		try {
			const r = await upload<{ hash: string; reply: string }>('PUT', `${fileURL(path)}&hash=${encodeURIComponent(hash)}`, new Blob([text], { type: 'application/json' }));
			d.saved = text;
			d.hash = r.hash;
			return r.reply;
		} catch (e) {
			if (asked || !(e instanceof ApiError && e.status === 409)) {
				toast.error(e);
				return null;
			}
			const now = String(e.body.hash ?? '');
			const overwrite = await ask({
				title: `Overwrite ${path}?`,
				body: `${path} changed on disk since you opened it here. Saving puts your version in its place. Cancel keeps the one on disk; reloading the page shows it, and drops your changes.`,
				action: 'Overwrite'
			});
			return overwrite ? write(path, now, true) : null;
		}
	}

	const label = (path: string) => {
		const key = path.match(/^parks\/(.+)\.park\.json$/)?.[1];
		return key ? (keyMaps[key.toLowerCase()] ?? key) : 'Name, author and version';
	};

	function keys(e: KeyboardEvent) {
		if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 's') {
			e.preventDefault();
			save();
		}
	}
</script>

<svelte:window onkeydown={keys} />

<div class="@container mx-auto max-w-6xl p-4 sm:p-6">
	{#if !list}
		{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
	{:else if !mod?.parks?.length || mod.shared || mod.disabled}
		<div class="card space-y-3 p-4 text-sm text-muted">
			<p>
				{#if !mod}There's no mod called {folder} in this server's Mods.
				{:else if mod.shared}{mod.title} is linked from the shared mods, which every server using them reads, so it isn't edited from one server.
				{:else if mod.disabled}{mod.title} is disabled: enable it to edit its parks.
				{:else}{mod.title} has no parks.{/if}
			</p>
			<a href="/s/{ctx.id}/parks" class="btn"><Icon name="chevron" size={14} class="rotate-90" /> Parks</a>
		</div>
	{:else}
		<section class="card overflow-hidden p-0">
			<div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b-2 border-line px-3 py-3 sm:px-4">
				<a href="/s/{ctx.id}/parks" class="btn btn-ghost btn-sm"><Icon name="chevron" size={14} class="rotate-90" /> Parks</a>
				<div class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2">
					<h2 class="truncate">{mod.title}</h2>
					{#if mod.version}<span class="text-xs text-faint">v{mod.version}</span>{/if}
				</div>
				{#if canEdit}
					<!-- Where Save is, so it's seen when Save won't go. -->
					{#if invalid}
						<span class="flex min-w-0 basis-64 items-start gap-1.5 text-xs text-bad" role="alert">
							<Icon name="alert" size={12} class="mt-0.5 shrink-0" /><span class="min-w-0 break-words">Not valid JSON: {invalid}</span>
						</span>
					{:else}
						<span class="text-xs text-faint">A park that's up is read again as you save.</span>
					{/if}
					<button class="btn btn-primary" disabled={saving || !changed.length || blocked} title="Ctrl+S" onclick={save}>
						<Icon name="check" size={14} />
						{saving ? 'Saving…' : changed.length > 1 ? `Save ${changed.length} files` : 'Save'}
					</button>
				{:else}
					<span class="sticker">View only</span>
				{/if}
			</div>
			<div class="grid @3xl:grid-cols-[15rem_minmax(0,1fr)]">
				<nav aria-label="Files" class="border-b-2 border-line p-2 @3xl:border-r-2 @3xl:border-b-0">
					<ul class="flex flex-wrap gap-1 @3xl:flex-col">
						{#each files as path (path)}
							<li>
								<button
									class="flex items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-muted transition-colors hover:bg-raised hover:text-fg aria-[current=true]:bg-raised aria-[current=true]:text-fg @3xl:w-full"
									aria-current={open === path}
									onclick={() => openFile(path)}
								>
									<span class="min-w-0 flex-1">
										<span class="block truncate font-mono text-xs">{path}</span>
										<span class="block truncate text-[11px] text-faint">{label(path)}</span>
									</span>
									{#if changed.includes(path)}<span class="size-2 shrink-0 rounded-full bg-warn" title="Not saved"></span>{/if}
								</button>
							</li>
						{/each}
					</ul>
				</nav>
				<div class="h-[65vh] min-h-80 min-w-0">
					{#if open && doc}
						{#key open}
							<CodeEditor path={open} text={doc.text} readonly={!canEdit} onchange={(t) => doc && (doc.text = t)} />
						{/key}
					{:else}
						<p class="p-4 text-sm text-muted">Pick a file.</p>
					{/if}
				</div>
			</div>
			<p class="border-t-2 border-line px-3 py-2.5 text-xs text-faint sm:px-4">
				Each object is <code>{'{"id", "item", "position": [x, y, z], "rotation": [x, y, z, w]}'}</code>. Saving checks the park as the server reads it, and turns away
				one it would refuse.
			</p>
		</section>
	{/if}
</div>
