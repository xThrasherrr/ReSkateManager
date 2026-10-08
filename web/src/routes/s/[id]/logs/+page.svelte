<script lang="ts">
	import { api, type LogResult } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { bytes } from '#lib/format.js';
	import ConsoleView from '#lib/components/ConsoleView.svelte';
	import Icon from '#lib/components/Icon.svelte';

	const ctx = useLive();

	type Source = 'server' | 'console';
	const sources: { value: Source; label: string; help: string }[] = [
		{ value: 'server', label: 'Server log', help: "What the server writes to ReSkateServer.log, also when it ran without the manager." },
		{
			value: 'console',
			label: 'Console output',
			help: "Everything the console showed while the manager ran it (console.log): Steam's own lines too, which never reach the server's log, the manager's notes and the commands sent."
		}
	];
	type Range = '1h' | '6h' | '24h' | '7d' | 'all' | 'custom';
	const ranges: { value: Range; label: string; hours?: number }[] = [
		{ value: '1h', label: '1 h', hours: 1 },
		{ value: '6h', label: '6 h', hours: 6 },
		{ value: '24h', label: '24 h', hours: 24 },
		{ value: '7d', label: '7 days', hours: 168 },
		{ value: 'all', label: 'All' },
		{ value: 'custom', label: 'Custom' }
	];
	const filters: [string, string][] = [
		['all', 'All'],
		['chat', 'Chat'],
		['players', 'Joins'],
		['admin', 'Admin'],
		['anticheat', 'Anti-cheat'],
		['manager', 'Manager']
	];

	let source = $state<Source>('server');
	let range = $state<Range>('24h');
	let from = $state(''); // datetime-local, for a custom range
	let to = $state('');
	let group = $state('all');
	let search = $state('');
	let applied = $state(''); // the search, once submitted
	let now = $state(Date.now()); // where the presets count back from; Refresh moves it

	const secs = (local: string) => String(Math.floor(new Date(local).getTime() / 1000));
	const query = $derived.by(() => {
		const p = new URLSearchParams({ source, group });
		const hours = ranges.find((r) => r.value === range)?.hours;
		if (hours) p.set('from', String(Math.floor(now / 1000) - hours * 3600));
		if (range === 'custom') {
			if (from) p.set('from', secs(from));
			if (to) p.set('to', secs(to));
		}
		if (applied) p.set('q', applied);
		return p.toString();
	});

	let result = $state.raw<LogResult | null>(null);
	let loading = $state(false);
	let asked = 0;
	async function load(q: string) {
		const mine = ++asked;
		loading = true;
		const r = await attempt(() => api.get<LogResult>(`/instances/${ctx.id}/logs?${q}`));
		if (mine !== asked) return; // a newer query is on its way
		loading = false;
		if (r) result = r;
	}
	$effect(() => {
		load(query);
	});

	// The date goes in front of each time once the lines span more than a day.
	const entries = $derived.by(() => {
		const list = result?.entries ?? [];
		const day = (ms: number) => new Date(ms).toDateString();
		const [first, last] = [list[0], list.at(-1)];
		const dated = !!first && !!last && day(first.at) !== day(last.at);
		const pad = (n: number) => String(n).padStart(2, '0');
		return list.map((e) => {
			const d = new Date(e.at);
			const time = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
			return { ...e, stamp: dated ? `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${time}` : time };
		});
	});
	const files = $derived(result ? [...(result.files.server ?? []), ...(result.files.console ?? [])] : []);
	let showFiles = $state(false);
</script>

{#if ctx.can('console.view')}
	<div class="flex h-full flex-col">
		<div class="space-y-2 border-b-2 border-line bg-panel px-3 py-2 sm:px-4">
			<div class="flex flex-wrap items-center gap-2">
				<div class="seg" role="group" aria-label="Log">
					{#each sources as s (s.value)}
						<button class={source === s.value ? 'seg-on' : ''} aria-pressed={source === s.value} title={s.help} onclick={() => (source = s.value)}
							>{s.label}</button
						>
					{/each}
				</div>
				<div class="seg no-scrollbar max-w-full overflow-x-auto" role="group" aria-label="Time range">
					{#each ranges as r (r.value)}
						<button class={range === r.value ? 'seg-on' : ''} aria-pressed={range === r.value} onclick={() => (range = r.value)}>{r.label}</button>
					{/each}
				</div>
				<div class="flex gap-2 sm:ml-auto">
					<button class="btn btn-sm h-8" onclick={() => (now = Date.now())} disabled={loading} title="Read the logs again"><Icon name="restart" size={13} /> Refresh</button>
					<a class="btn btn-sm h-8" href="/api/instances/{ctx.id}/logs/export?{query}" download title="Every line that matches, as a file"
						><Icon name="download" size={13} /> Export</a
					>
					<button class="btn btn-sm h-8" aria-expanded={showFiles} onclick={() => (showFiles = !showFiles)}><Icon name="list" size={13} /> Files</button>
				</div>
			</div>
			{#if range === 'custom'}
				<div class="flex flex-wrap items-center gap-2 text-xs">
					<label class="flex items-center gap-1.5">From <input type="datetime-local" class="input h-8 w-auto text-xs" bind:value={from} /></label>
					<label class="flex items-center gap-1.5">to <input type="datetime-local" class="input h-8 w-auto text-xs" bind:value={to} /></label>
					<span class="text-faint">On this browser's clock. Leave one empty for no limit.</span>
				</div>
			{/if}
			<div class="flex flex-wrap items-center gap-2">
				<div class="seg no-scrollbar max-w-full overflow-x-auto" role="group" aria-label="Show">
					{#each filters as [key, label] (key)}
						<button class={group === key ? 'seg-on' : ''} aria-pressed={group === key} onclick={() => (group = key)}>{label}</button>
					{/each}
				</div>
				<form
					class="relative w-full sm:ml-auto sm:w-64"
					onsubmit={(e) => {
						e.preventDefault();
						applied = search.trim();
					}}
				>
					<Icon name="search" size={14} class="absolute top-1/2 left-2 -translate-y-1/2 text-faint" />
					<input class="input h-8 pl-7 text-xs" placeholder="Search, then Enter" aria-label="Search the logs" bind:value={search} />
				</form>
			</div>
			{#if showFiles}
				<div class="rounded-xl border-2 border-line bg-sunken/50 p-2 text-xs">
					{#if !result}
						<p class="font-marker text-faint">loading…</p>
					{:else if files.length === 0}
						<p class="text-faint">No log files yet.</p>
					{:else}
						<ul class="grid gap-1 sm:grid-cols-2">
							{#each files as f (f.name)}
								<li class="flex items-center gap-2">
									<a class="font-mono text-accent hover:underline" href="/api/instances/{ctx.id}/logs/files/{encodeURIComponent(f.name)}" download>{f.name}</a>
									<span class="text-faint tabular-nums">{bytes(f.size)} · {new Date(f.modified).toLocaleString()}</span>
								</li>
							{/each}
						</ul>
					{/if}
					<p class="mt-2 text-faint">Each log moves to .1 at 10 MB, keeping three old ones. console.log holds what the console showed while the manager ran the server.</p>
				</div>
			{/if}
		</div>
		<div class="min-h-0 flex-1">
			<ConsoleView {entries} empty={loading ? 'Reading the logs…' : 'Nothing in this range.'} />
		</div>
		<div class="flex flex-wrap items-center gap-x-3 border-t-2 border-line bg-panel px-3 py-1.5 text-xs text-faint sm:px-4">
			{#if loading}
				<span class="font-marker">reading…</span>
			{:else if result}
				{#if result.total > result.entries.length}
					<span>The newest {result.entries.length.toLocaleString()} of {result.total.toLocaleString()} matching lines. Narrow it down, or export them all.</span>
				{:else}
					<span>{result.total.toLocaleString()} line{result.total === 1 ? '' : 's'}</span>
				{/if}
			{/if}
			<span class="ml-auto hidden sm:inline">{sources.find((s) => s.value === source)?.help}</span>
		</div>
	</div>
{:else}
	<div class="p-6 text-sm text-muted">You don't have access to this server's logs.</div>
{/if}
