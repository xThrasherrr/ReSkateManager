<script lang="ts">
	import { api, message, type DiskKind, type HostDisk } from '#lib/api.js';
	import { instances } from '#lib/instances.svelte.js';
	import { toast } from '#lib/toast.svelte.js';
	import { ago, bytes } from '#lib/format.js';
	import Icon from './Icon.svelte';
	import InfoTip from './InfoTip.svelte';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';

	// The Host page's disk card: each drive the manager keeps things on, and
	// what its folders hold there.
	let data = $state.raw<HostDisk | null>(null);
	let error = $state('');
	let misses = $state(0);

	async function load(fresh = false) {
		try {
			data = await api.get<HostDisk>(`/host/disk${fresh ? '?fresh' : ''}`);
			error = '';
			misses = 0;
		} catch (err) {
			error = message(err);
			misses++;
			if (fresh && data) toast.error(err);
		}
	}
	// Free space is read on every look, so keep up with it; while the folders
	// are being measured, look again soon, and after a miss, in a while.
	$effect(() => {
		const t = setTimeout(load, misses ? 15_000 : !data ? 0 : data.measuring ? 1000 : 30_000);
		return () => clearTimeout(t);
	});

	const kinds: { kind: DiskKind; label: string; color: string }[] = [
		{ kind: 'servers', label: 'Servers', color: 'var(--color-info)' },
		{ kind: 'shared', label: 'Shared mods', color: 'var(--color-ok)' },
		{ kind: 'backups', label: 'Backups', color: 'var(--color-warn)' },
		{ kind: 'cache', label: 'Server releases', color: 'var(--color-pink)' },
		{ kind: 'logs', label: 'Logs', color: '#ff9147' },
		{ kind: 'data', label: 'Manager data', color: '#2dd4bf' }
	];

	const drives = $derived(
		(data?.drives ?? []).map((d) => {
			const here = data!.parts.filter((p) => p.volume === d.volume);
			const parts = kinds.filter((k) => here.some((p) => p.kind === k.kind)).map((k) => ({ ...k, size: here.filter((p) => p.kind === k.kind).reduce((a, p) => a + p.size, 0) }));
			// The bar spans what the manager could fill, as df's Use% does: on
			// Linux the blocks kept for root aren't in it.
			const span = Math.max(1, d.used + d.free);
			const sum = parts.reduce((a, p) => a + p.size, 0);
			const ours = Math.min(d.used, sum);
			return { ...d, parts, sum, ours, other: d.used - ours, span, low: d.free < data!.lowFree };
		})
	);
	const servers = $derived(data?.servers ?? []);
	const fresh = $derived(!!data?.at && Date.now() / 1000 - data.at < 30);
	const share = (n: number, of: number) => `${(n / of) * 100}%`;
</script>

<section class="card">
	<div class="flex flex-wrap items-center gap-x-1 gap-y-2 border-b-2 border-line px-4 py-3">
		<h2>Disk</h2>
		<InfoTip
			text="Each drive the manager keeps things on: how full it is, and what the manager's folders hold there. Everything else is what the rest of the machine keeps on it. The folders are measured when someone looks, at most every 10 minutes, since adding up big Mods folders takes a while. A shared mod counts once, under Shared mods, however many servers use it."
			label="About disk"
		/>
		<div class="ml-auto flex items-center gap-2 text-xs text-faint">
			{#if data?.measuring}
				<span>measuring…</span>
			{:else if data?.at}
				<span>measured {ago(data.at * 1000)}</span>
			{/if}
			<button class="btn btn-sm btn-ghost" disabled={!data || data.measuring || fresh} onclick={() => load(true)}>
				<Icon name="restart" size={12} /> Measure again
			</button>
		</div>
	</div>

	{#if !data}
		{#if error}<LoadError class="m-4" {error} onretry={() => load()} />{:else}<Loading class="px-4 py-6" />{/if}
	{/if}
	{#if data && !drives.length}
		<p class="px-4 py-6 text-sm text-faint">The manager can't read its disk space on this system.</p>
	{/if}
	{#each drives as d (d.volume)}
		<div class="space-y-3 border-b-2 border-line px-4 py-4 last:border-b-0">
			<div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
				<span class="font-mono text-sm break-all">{d.volume}</span>
				<span class="text-sm tabular-nums {d.low ? 'font-semibold text-bad' : 'text-muted'}" title={d.low ? `Under the ${bytes(data!.lowFree)} the disk alert is set to` : undefined}>
					{bytes(d.free)} free of {bytes(d.total)}{#if d.low}<span class="sticker sticker-pink ml-2">low</span>{/if}
				</span>
			</div>
			<div class="flex h-4 overflow-hidden rounded-full border-2 border-ink bg-sunken" role="img" aria-label="{bytes(d.used)} used, {bytes(d.free)} free">
				<div class="h-full bg-accent" style:width={share(d.ours, d.span)}></div>
				<div class="tape-stripes h-full bg-line-strong" style:width={share(d.other, d.span)}></div>
			</div>
			<ul class="flex flex-wrap gap-x-6 gap-y-1 text-sm">
				{#if d.parts.length}
					<li class="flex items-center gap-2">
						<span class="size-2.5 shrink-0 rounded-sm bg-accent"></span><span class="text-muted">The manager's folders</span><span class="tabular-nums">{bytes(d.sum)}</span>
					</li>
				{/if}
				<li class="flex items-center gap-2">
					<span class="tape-stripes size-2.5 shrink-0 rounded-sm bg-line-strong"></span><span class="text-muted">{d.parts.length ? 'Everything else' : 'In use'}</span><span class="tabular-nums">{bytes(d.other)}</span>
				</li>
				<li class="flex items-center gap-2">
					<span class="size-2.5 shrink-0 rounded-sm border border-line-strong bg-sunken"></span><span class="text-muted">Free</span><span class="tabular-nums">{bytes(d.free)}</span>
				</li>
			</ul>
			{#if d.parts.length}
				<div class="space-y-2 rounded-xl bg-sunken/60 p-3">
					<div class="flex items-baseline justify-between gap-4 text-xs">
						<span class="label mb-0">The manager's folders</span>
						<span class="text-muted tabular-nums">{bytes(d.sum)}</span>
					</div>
					<div class="flex h-2.5 overflow-hidden rounded-full border-2 border-ink bg-sunken" role="img" aria-label="The manager's folders, by part">
						{#each d.parts as p (p.kind)}
							<div class="h-full" style:width={share(p.size, Math.max(1, d.sum))} style:background={p.color} title="{p.label}: {bytes(p.size)}"></div>
						{/each}
					</div>
					<ul class="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2 lg:grid-cols-3">
						{#each d.parts as p (p.kind)}
							<li class="flex items-center gap-2">
								<span class="size-2.5 shrink-0 rounded-sm" style:background={p.color}></span>
								<span class="text-muted">{p.label}</span>
								<span class="ml-auto tabular-nums">{bytes(p.size)}</span>
							</li>
						{/each}
					</ul>
				</div>
			{:else if data?.measuring}
				<p class="text-sm text-faint">Measuring what the manager's folders hold…</p>
			{/if}
		</div>
	{/each}

	{#if servers.length}
		<div class="overflow-x-auto">
			<table class="table">
				<thead>
					<tr>
						<th>Server</th>
						<th>Program and config</th>
						<th>Own mods</th>
						<th>Logs</th>
						<th>Total</th>
						{#if drives.length > 1}<th>Drive</th>{/if}
					</tr>
				</thead>
				<tbody>
					{#each servers as s (s.id)}
						<tr>
							<td class="whitespace-nowrap">
								{#if instances.byId(s.id)}<a href="/s/{s.id}" class="hover:underline">{s.name}</a>{:else}{s.name}{/if}
							</td>
							<td class="tabular-nums">{bytes(s.files)}</td>
							<td class="tabular-nums">{bytes(s.mods)}</td>
							<td class="tabular-nums">{bytes(s.logs)}</td>
							<td class="tabular-nums">{bytes(s.files + s.mods + s.logs)}</td>
							{#if drives.length > 1}<td class="font-mono text-xs">{s.volume}</td>{/if}
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</section>
