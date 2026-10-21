<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError, message, sleep, upload, type PluginFile, type PluginsInfo } from '#lib/api.js';
	import { ask } from '#lib/ask.svelte.js';
	import { toast } from '#lib/toast.svelte.js';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';
	import CodeEditor from '#lib/components/CodeEditor.svelte';
	import Icon from '#lib/components/Icon.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import { pluginLog, sticker } from '#lib/components/PluginSettings.svelte';

	// A file open here: its text in the editor, its text on disk, and the hash
	// the manager gave for that, '' for a file not saved yet.
	interface Doc {
		text: string;
		saved: string;
		hash: string;
	}

	const pid = $derived(page.params.pid ?? '');
	const base = $derived(`/manager/plugins/${encodeURIComponent(pid)}`);
	const fileURL = (path: string) => `${base}/file?path=${encodeURIComponent(path)}`;

	let info = $state.raw<PluginsInfo | null>(null);
	let files = $state.raw<PluginFile[] | null>(null);
	let loadError = $state('');
	let docs = $state<Record<string, Doc>>({});
	let open = $state<string | null>(null);
	let saving = $state(false);
	let naming = $state(false);
	let newPath = $state('');

	const plugin = $derived(info?.plugins.find((p) => p.id === pid));
	const dirty = (d: Doc) => d.text !== d.saved || !d.hash;
	const changed = $derived(Object.entries(docs).flatMap(([p, d]) => (dirty(d) ? [p] : [])));
	const doc = $derived(open ? docs[open] : undefined);
	// main.lua and plugin.toml first, then the rest as paths sort, new files with them.
	const rank = (p: string) => (p === 'main.lua' ? 0 : p === 'plugin.toml' ? 1 : 2);
	const listed = $derived(
		[...new Set([...(files ?? []).map((f) => f.path), ...Object.keys(docs)])].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
	);

	guardUnsaved(() => changed.length > 0, 'Some files here aren’t saved yet; leaving drops your changes.');

	// Another plugin's editor, by the browser's back and forward, starts afresh.
	$effect(() => {
		pid;
		untrack(() => {
			docs = {};
			open = null;
			files = null;
			load();
		});
	});

	async function load() {
		try {
			[info, files] = await Promise.all([api.get<PluginsInfo>('/manager/plugins'), api.get<PluginFile[]>(`${base}/files`)]);
			loadError = '';
			const first = files.find((f) => f.path === 'main.lua') ?? files[0];
			if (!open && first) await openFile(first.path);
		} catch (e) {
			loadError = message(e);
		}
	}

	onMount(() => {
		// Its state and log, to see what a save did.
		const t = setInterval(async () => {
			if (!document.hidden && !saving) info = await api.get<PluginsInfo>('/manager/plugins').catch(() => info);
		}, 3000);
		return () => clearInterval(t);
	});

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

	// Writes every changed file, then reloads the plugin once so it runs them.
	async function save() {
		if (saving || !changed.length) return;
		saving = true;
		const name = plugin?.name ?? pid;
		const saved: string[] = [];
		for (const path of changed) {
			if (!(await write(path))) break;
			saved.push(path);
		}
		if (saved.length) {
			const what = saved.length === 1 ? saved[0] : `${saved.length} files`;
			try {
				await api.post(`${base}/reload`);
				await settle();
				if (!plugin?.enabled) toast.ok(`Saved ${what}`);
				else if (plugin.state === 'running') toast.ok(`Saved ${what} and restarted ${name}`);
				else toast.error(`Saved ${what}, but ${name} didn't start: its log below says why`);
			} catch (e) {
				toast.error(`Saved ${what}, but ${name} didn't reload: ${message(e)}`);
			}
			files = await api.get<PluginFile[]>(`${base}/files`).catch(() => files);
		}
		saving = false;
	}

	// Saves one file over the hash it was read with, or over hash once they
	// say so after it changed on disk.
	async function write(path: string, hash?: string, asked = false): Promise<boolean> {
		const d = docs[path];
		if (!d) return false;
		hash ??= d.hash;
		const text = d.text; // what's typed while it saves stays unsaved
		try {
			const r = await upload<{ hash: string }>('PUT', `${fileURL(path)}&hash=${encodeURIComponent(hash)}`, new Blob([text], { type: 'text/plain' }));
			d.saved = text;
			d.hash = r.hash;
			return true;
		} catch (e) {
			if (asked || !(e instanceof ApiError && e.status === 409)) {
				toast.error(e);
				return false;
			}
			const now = String(e.body.hash ?? '');
			const overwrite = await ask({
				title: `Overwrite ${path}?`,
				body: d.hash
					? `${path} changed on disk since you opened it here${now ? '' : ', and is gone now'}. Saving puts your version in its place. Cancel keeps the one on disk; reloading the page shows it, and drops your changes.`
					: `There's a ${path} already. Saving puts yours in its place.`,
				action: 'Overwrite'
			});
			return overwrite && write(path, now, true);
		}
	}

	// After a reload, until it's running or has failed.
	async function settle() {
		for (let i = 0; i < 40; i++) {
			info = await api.get<PluginsInfo>('/manager/plugins').catch(() => info);
			if (plugin?.state !== 'starting') return;
			await sleep(250);
		}
	}

	// A path in the plugin's folder: slashes between folders, no part hidden.
	const pathRe = /^[^./\\:][^/\\:]*(\/[^./\\:][^/\\:]*)*$/;

	function addFile(e: SubmitEvent) {
		e.preventDefault();
		const path = newPath.trim();
		if (!pathRe.test(path)) {
			toast.error('Name it like lib/util.lua: slashes between folders, and no part starting with a dot.');
			return;
		}
		if (!listed.includes(path)) docs[path] = { text: '', saved: '', hash: '' };
		naming = false;
		newPath = '';
		openFile(path);
	}

	function keys(e: KeyboardEvent) {
		if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 's') {
			e.preventDefault();
			save();
		}
	}
</script>

<svelte:window onkeydown={keys} />

{#if !files || !info}
	{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
{:else}
	<section class="card overflow-hidden p-0">
		<div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b-2 border-line px-3 py-3 sm:px-4">
			<a href="/manager/plugins" class="btn btn-ghost btn-sm"><Icon name="chevron" size={14} class="rotate-90" /> Plugins</a>
			<div class="flex min-w-0 flex-1 flex-wrap items-center gap-2">
				<h2 class="truncate">{plugin?.name ?? pid}</h2>
				{#if plugin?.version}<span class="text-xs text-faint">{plugin.version}</span>{/if}
				{#if plugin}<span class="sticker {sticker[plugin.state][0]}">{sticker[plugin.state][1]}</span>{/if}
			</div>
			<span class="text-xs text-faint">{plugin?.enabled ? 'Saving restarts it with your changes.' : 'It’s off: saving writes the files.'}</span>
			<button class="btn btn-primary" disabled={saving || !changed.length} title="Ctrl+S" onclick={save}>
				<Icon name="check" size={14} />
				{saving ? 'Saving…' : changed.length > 1 ? `Save ${changed.length} files` : 'Save'}
			</button>
		</div>
		<div class="grid @3xl:grid-cols-[15rem_minmax(0,1fr)]">
			<nav aria-label="Files" class="space-y-2 border-b-2 border-line p-2 @3xl:border-r-2 @3xl:border-b-0">
				<ul class="flex flex-wrap gap-1 @3xl:flex-col">
					{#each listed as path (path)}
						<li>
							<button
								class="flex items-center gap-2 rounded-lg px-2.5 py-1.5 text-left font-mono text-xs text-muted transition-colors hover:bg-raised hover:text-fg aria-[current=true]:bg-raised aria-[current=true]:text-fg @3xl:w-full"
								aria-current={open === path}
								onclick={() => openFile(path)}
							>
								<span class="min-w-0 flex-1 truncate">{path}</span>
								{#if changed.includes(path)}<span class="size-2 shrink-0 rounded-full bg-warn" title="Not saved"></span>{/if}
							</button>
						</li>
					{/each}
				</ul>
				{#if naming}
					<form class="px-1" onsubmit={addFile}>
						<!-- svelte-ignore a11y_autofocus -->
						<input
							class="input font-mono text-xs"
							placeholder="lib/util.lua"
							aria-label="New file's path"
							autofocus
							bind:value={newPath}
							onkeydown={(e) => e.key === 'Escape' && (naming = false)}
							onblur={() => !newPath.trim() && (naming = false)}
						/>
					</form>
				{:else}
					<button class="btn btn-ghost btn-sm" onclick={() => (naming = true)}><Icon name="plus" size={14} /> New file</button>
				{/if}
			</nav>
			<div class="h-[65vh] min-h-80 min-w-0">
				{#if open && doc}
					{#key open}
						<CodeEditor path={open} text={doc.text} onchange={(t) => doc && (doc.text = t)} />
					{/key}
				{:else}
					<p class="p-4 text-sm text-muted">{listed.length ? 'Pick a file.' : 'Its folder is empty: add a file.'}</p>
				{/if}
			</div>
		</div>
		<div class="space-y-2 border-t-2 border-line px-3 py-3 sm:px-4">
			{#if plugin?.problem}
				<p class="flex items-start gap-1.5 text-sm text-bad"><Icon name="alert" size={14} class="mt-0.5 shrink-0" /> {plugin.problem}</p>
			{:else if plugin?.reason && (plugin.state === 'failed' || plugin.state === 'stopped')}
				<p class="flex items-start gap-1.5 text-sm text-warn"><Icon name="alert" size={14} class="mt-0.5 shrink-0" /> <span class="min-w-0 break-words">{plugin.reason}</span></p>
			{/if}
			<h3 class="text-xs font-semibold text-muted">Log</h3>
			{#if plugin?.recent.length}
				{@render pluginLog(plugin.recent)}
			{:else}
				<p class="text-xs text-faint">Nothing logged yet.</p>
			{/if}
		</div>
	</section>
{/if}
