<script lang="ts">
	import type { HostPerf } from '#lib/api.js';
	import { instances } from '#lib/instances.svelte.js';
	import { session } from '#lib/session.svelte.js';
	import { Samples } from '#lib/samples.svelte.js';
	import { memory, memoryTop, niceTop, percent } from '#lib/format.js';
	import StackChart, { type Series } from '#lib/components/StackChart.svelte';
	import ChartCard from '#lib/components/ChartCard.svelte';
	import InfoTip from '#lib/components/InfoTip.svelte';
	import RangePicker from '#lib/components/RangePicker.svelte';
	import StatTile from '#lib/components/StatTile.svelte';
	import DiskUsage from '#lib/components/DiskUsage.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';

	const samples = new Samples<HostPerf>((range) => `/host?range=${range}`);
	const data = $derived(samples.data);
	const mid = samples.mid;
	const points = $derived(data?.points ?? []);
	let hover = $state<number | null>(null);

	// Each server keeps its colour across charts, the table and ranges (the list is sorted by name).
	const palette = ['var(--color-accent)', 'var(--color-pink)', 'var(--color-ok)', 'var(--color-info)', 'var(--color-warn)', '#ff9147', '#2dd4bf', '#f0abfc'];
	const servers = $derived((data?.servers ?? []).map((s, i) => ({ ...s, color: palette[i % palette.length] ?? 'var(--color-accent)' })));
	const ran = $derived(servers.filter((s) => s.points.length));

	const cached = $derived(points.some((p) => p.memCached > 0));
	const limited = $derived(points.some((p) => p.limitMax > 0));
	const cpuLines = $derived<Series[]>([
		{ label: 'Machine', color: 'var(--color-fg)', points: points.map((p) => ({ at: mid(p.at), v: p.cpu, hi: p.cpuMax })), band: (_, hi) => `peak ${percent(hi)}` }
	]);
	const cpuLayers = $derived<Series[]>(ran.map((s) => ({ label: s.name, color: s.color, points: s.points.map((p) => ({ at: mid(p.at), v: p.cpu })) })));
	const memLines = $derived<Series[]>([
		{
			label: 'In use',
			color: 'var(--color-fg)',
			points: points.map((p) => ({ at: mid(p.at), v: p.memUsed, hi: p.memUsed + p.memCached })),
			band: (v, hi) => `${memory(hi - v)} cached`
		},
		...(limited
			? [{ label: 'Container limit', color: 'var(--color-bad)', dash: true, points: points.filter((p) => p.limitMax > 0).map((p) => ({ at: mid(p.at), v: p.limitMax })) }]
			: [])
	]);
	const memLayers = $derived<Series[]>([
		...ran.map((s) => ({ label: s.name, color: s.color, points: s.points.map((p) => ({ at: mid(p.at), v: p.mem })) })),
		{ label: 'Manager', color: 'var(--color-muted)', points: points.map((p) => ({ at: mid(p.at), v: p.managerMem })) }
	]);
	const playerLines = $derived<Series[]>([{ label: 'Players', color: 'var(--color-pink)', points: points.map((p) => ({ at: mid(p.at), v: p.players })) }]);
	const memTotal = $derived(Math.max(0, ...points.map((p) => p.memTotal)));

	// The tiles' headline is the newest sample, while it is recent.
	const fresh = $derived(data?.latest && data.latest.at >= samples.until - 3 * data.interval ? data.latest : null);
	const avg = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 0);
	const stats = $derived({
		cpuAvg: avg(points.map((p) => p.cpu)),
		cpuPeak: Math.max(0, ...points.map((p) => p.cpuMax)),
		memAvg: avg(points.map((p) => p.memUsed)),
		memPeak: Math.max(0, ...points.map((p) => p.memUsed)),
		playersPeak: Math.max(0, ...points.map((p) => p.players))
	});

	// A tile's line: the parts there are, joined with dots.
	const join = (...parts: unknown[]) => parts.filter((p) => typeof p === 'string' && p).join(' · ');
	const none = 'no samples in this range';
	const tiles = $derived<{ label: string; info?: string; now: string; of?: string; sub: string[] }[]>([
		{
			label: 'CPU',
			now: fresh ? percent(fresh.cpu) : '—',
			sub: [points.length ? join(fresh && `servers ${percent(fresh.serversCpu)}`, `avg ${percent(stats.cpuAvg)}`, `peak ${percent(stats.cpuPeak)}`) : none]
		},
		{
			label: 'Memory in use',
			now: fresh ? memory(fresh.memUsed) : '—',
			of: fresh ? memory(fresh.memTotal) : undefined,
			sub: [
				points.length ? join(fresh?.memCached && `${memory(fresh.memCached)} cached`, `peak ${memory(stats.memPeak)}`) : none,
				...(fresh?.limitMax ? [`container ${memory(fresh.limitUsed)} of ${memory(fresh.limitMax)}`] : [])
			]
		},
		{
			label: 'Servers running',
			now: fresh ? String(fresh.running) : '—',
			of: fresh ? String(fresh.servers) : undefined,
			sub: [points.length ? join(fresh && `${fresh.players} ${fresh.players === 1 ? 'player' : 'players'} online`, `peak ${stats.playersPeak}`) : none]
		},
		{
			label: 'Manager',
			info: 'Memory the manager itself holds in RAM, the panel and its database included. The servers it runs are counted apart.',
			now: fresh ? memory(fresh.managerMem) : '—',
			sub: [points.length ? `avg ${memory(avg(points.map((p) => p.managerMem)))}` : none]
		}
	]);

	const charts = $derived([
		{
			title: 'CPU',
			info: "The whole machine's CPU, all cores together (the line), and each server's share of it (the areas). The gap between them is everything else on the machine, the manager included. The faint band reaches the highest reading within each point.",
			lines: cpuLines,
			layers: cpuLayers,
			ceil: (v: number) => niceTop(v, 5),
			top: undefined,
			format: percent
		},
		{
			title: 'Memory',
			info:
				'Memory in use on the whole machine (the line): its total less what programs could still get. ' +
				(cached ? 'Cache the system hands back when asked counts as free; the faint band above the line shows how much of it there is. ' : '') +
				'The areas are each server and the manager; the gap up to the line is everything else on the machine.' +
				(limited ? " The dashed line is the memory limit of the manager's container." : ''),
			lines: memLines,
			layers: memLayers,
			ceil: memoryTop,
			top: memTotal || undefined,
			format: memory
		},
		{
			title: 'Players',
			info: 'Players online on every server together, the most at once within each point.',
			lines: playerLines,
			layers: [],
			ceil: (v: number) => niceTop(v, 4),
			top: undefined,
			format: (v: number) => String(Math.round(v))
		}
	]);
</script>

<div class="mx-auto max-w-6xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	<div class="flex flex-wrap items-end justify-between gap-4">
		<div>
			<h1 class="page-title">Host</h1>
			<p class="mt-2 text-sm text-muted">The machine the manager runs on: what is using its CPU, memory and disk.</p>
		</div>
		<RangePicker bind:value={samples.range} class="no-scrollbar max-w-full overflow-x-auto" />
	</div>

	{#if samples.error}<LoadError error={samples.error} onretry={() => samples.load()} />{/if}

	<div class="grid gap-3 sm:grid-cols-2 sm:gap-4 lg:grid-cols-4">
		{#each tiles as t (t.label)}<StatTile {...t} />{/each}
	</div>

	{#if !data}
		{#if !samples.error}<Loading class="card px-4 py-8 text-center" />{/if}
	{:else if !points.length}
		<p class="card px-4 py-8 text-center font-marker text-faint">
			{samples.range === '1h' ? 'no samples yet. the first lands about 30 seconds after the manager starts.' : 'no samples in this range.'}
		</p>
	{:else}
		{#each charts as c (c.title)}
			<ChartCard title={c.title} info={c.info}>
				<StackChart
					lines={c.lines}
					layers={c.layers}
					since={samples.since}
					until={samples.until}
					bucket={samples.bucket}
					ticks={samples.ticks}
					ceil={c.ceil}
					top={c.top}
					format={c.format}
					bind:hover
					label="{c.title}, last {samples.rangeName}"
				/>
			</ChartCard>
		{/each}
	{/if}

	<section class="card">
		<div class="flex items-center gap-1 border-b-2 border-line px-4 py-3">
			<h2>By server</h2>
			<InfoTip text="Each server's own process while it ran in this range: the averages leave out the time it was stopped." label="About the server table" />
		</div>
		{#if servers.length}
			<div class="overflow-x-auto">
				<table class="table">
					<thead><tr><th>Server</th><th>Avg CPU</th><th>Peak CPU</th><th>Avg memory</th><th>Peak memory</th></tr></thead>
					<tbody>
						{#each servers as s (s.id)}
							<tr>
								<td class="whitespace-nowrap">
									<span class="mr-1.5 inline-block size-2.5 rounded-sm align-middle opacity-80" style:background={s.color}></span>
									{#if instances.byId(s.id)}<a href="/s/{s.id}/performance" class="hover:underline">{s.name}</a>{:else}{s.name}{/if}
								</td>
								{#if s.points.length}
									<td class="tabular-nums">{percent(s.cpu)}</td>
									<td class="tabular-nums">{percent(s.cpuMax)}</td>
									<td class="tabular-nums">{memory(s.mem)}</td>
									<td class="tabular-nums">{memory(s.memMax)}</td>
								{:else}
									<td colspan="4" class="text-faint">didn't run in this range</td>
								{/if}
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{:else if data}
			<p class="px-4 py-6 text-sm text-faint">No servers yet.{#if session.can('instances.manage')} <a href="/?new" class="text-accent hover:underline">Add one</a> on the dashboard.{/if}</p>
		{/if}
	</section>

	<DiskUsage />

	<p class="text-xs text-faint">CPU, memory and players sampled every {data?.interval ?? 30}s; kept for 7 days.</p>
</div>
