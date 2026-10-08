<script lang="ts">
	import type { NetPoint, Perf } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { Samples } from '#lib/samples.svelte.js';
	import { dateTime, duration, memory, memoryTop, niceTop, percent } from '#lib/format.js';
	import StackChart, { type Series } from '#lib/components/StackChart.svelte';
	import ChartCard from '#lib/components/ChartCard.svelte';
	import InfoTip from '#lib/components/InfoTip.svelte';
	import RangePicker from '#lib/components/RangePicker.svelte';
	import StatTile from '#lib/components/StatTile.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';

	const ctx = useLive();
	const live = $derived(ctx.live);

	const samples = new Samples<Perf>((range) => `/instances/${ctx.id}/perf?range=${range}`);
	const data = $derived(samples.data);
	let hover = $state<number | null>(null);

	const points = $derived(data?.points ?? []);
	// One line per chart; it breaks where the process started again.
	const line = (label: string, color: string, pick: (p: Perf['points'][number]) => { v: number; hi?: number }, band?: Series['band']): Series[] => [
		{ label, color, band, points: points.map((p) => ({ at: samples.mid(p.at), run: p.run, ...pick(p) })) }
	];

	const running = $derived(live.state?.state === 'running' || live.state?.state === 'starting');
	const current = (run: number) => running && live.state?.startedAt != null && Math.floor(live.state.startedAt / 1000) === run;
	// The tiles' headline is the newest sample, while it belongs to the run that is up now.
	const fresh = $derived(data?.latest && current(data.latest.run) && data.latest.at >= samples.until - 3 * data.interval ? data.latest : null);
	const avg = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 0);
	const none = $derived(data ? 'no samples in this range' : '');
	const tiles = $derived(
		points.length
			? [
					{ label: 'CPU', now: fresh ? percent(fresh.cpu) : '—', sub: [`avg ${percent(avg(points.map((p) => p.cpu)))} · peak ${percent(Math.max(0, ...points.map((p) => p.cpuMax)))}`] },
					{ label: 'Memory', now: fresh ? memory(fresh.mem) : '—', sub: [`avg ${memory(avg(points.map((p) => p.mem)))} · peak ${memory(Math.max(0, ...(data?.runs ?? []).map((r) => r.memMax)))}`] },
					{ label: 'Players', now: fresh ? String(fresh.players) : '—', sub: [`peak ${Math.max(0, ...points.map((p) => p.players))}`] }
				]
			: [
					{ label: 'CPU', now: '—', sub: [none] },
					{ label: 'Memory', now: '—', sub: [none] },
					{ label: 'Players', now: '—', sub: [none] }
				]
	);

	const charts = $derived([
		{
			title: 'CPU',
			info: "The server process's share of the whole machine, all cores together. The faint band reaches the highest reading within each point.",
			lines: line('CPU', 'var(--color-accent)', (p) => ({ v: p.cpu, hi: p.cpuMax }), (_, hi) => `peak ${percent(hi)}`),
			ceil: (v: number) => niceTop(v, 5),
			format: percent
		},
		{
			title: 'Memory',
			info: 'Memory the server process holds in RAM (working set on Windows, RSS on Linux).',
			lines: line('Memory', 'var(--color-info)', (p) => ({ v: p.mem })),
			ceil: memoryTop,
			format: memory
		},
		{
			title: 'Players',
			info: 'The most players online at once within each point.',
			lines: line('Players', 'var(--color-pink)', (p) => ({ v: p.players })),
			ceil: (v: number) => niceTop(v, 4),
			format: (v: number) => String(Math.round(v))
		}
	]);

	// ---- the network, from the server's own summaries ----
	const net = $derived(data?.network ?? []);
	// A line of what each point has; points without it (older servers) are left out.
	const along = (pick: (p: NetPoint) => number | null) =>
		net.flatMap((p) => {
			const v = pick(p);
			return v == null ? [] : [{ at: samples.mid(p.at), v }];
		});
	const has = (pick: (p: NetPoint) => number | null) => net.some((p) => pick(p) != null);
	const kbs = (v: number) => `${Math.round(v)} KB/s`;
	const ms = (v: number) => `${v < 10 && v % 1 ? v.toFixed(1) : Math.round(v)} ms`;
	const netCharts = $derived([
		{
			title: 'Bandwidth',
			info: 'What the server sends to and gets from all players together, averaged within each point. The faint band reaches the busiest minute.',
			lines: [
				{ label: 'Out', color: 'var(--color-accent)', points: net.map((p) => ({ at: samples.mid(p.at), v: p.out, hi: p.outMax })), band: (_: number, hi: number) => `peak ${kbs(hi)}` },
				{ label: 'In', color: 'var(--color-info)', points: net.map((p) => ({ at: samples.mid(p.at), v: p.in, hi: p.inMax })), band: (_: number, hi: number) => `peak ${kbs(hi)}` }
			],
			ceil: (v: number) => niceTop(v, 10),
			format: kbs
		},
		{
			title: 'Latency',
			info: "The worst player's ping, and the longest a message would wait in the server's send queue. A wait that grows with the player count is the server not getting its data out fast enough.",
			lines: [
				{ label: 'Worst ping', color: 'var(--color-pink)', points: along((p) => p.ping) },
				...(has((p) => p.queueMs) ? [{ label: 'Queue wait', color: 'var(--color-warn)', points: along((p) => p.queueMs) }] : [])
			],
			ceil: (v: number) => niceTop(v, 100),
			format: ms
		},
		...(has((p) => p.failed)
			? [
					{
						title: 'Sends lost',
						info: 'Messages within each point that the server failed to send, dropped, or skipped because a newer one replaced them. A few skipped is normal when it is busy; failed or dropped sends are things players missed.',
						lines: [
							{ label: 'Failed', color: 'var(--color-bad)', points: along((p) => p.failed) },
							{ label: 'Dropped', color: 'var(--color-warn)', points: along((p) => p.dropped) },
							{ label: 'Skipped', color: 'var(--color-muted)', points: along((p) => p.skipped) }
						],
						ceil: (v: number) => niceTop(v, 4),
						format: (v: number) => String(Math.round(v))
					}
				]
			: []),
		...(has((p) => p.busy)
			? [
					{
						title: 'Server loop',
						info: "How much of each second the server's main loop spends working, the most within each point. Near 100% it can't keep up, and play stutters for everyone.",
						lines: [{ label: 'Busy', color: 'var(--color-ok)', points: along((p) => p.busy) }],
						ceil: () => 100,
						format: percent
					}
				]
			: [])
	]);
</script>

<div class="space-y-4 p-4 sm:space-y-6 sm:p-6">
	<div class="flex flex-wrap items-center gap-3">
		<RangePicker bind:value={samples.range} />
		<span class="text-xs text-faint">Sampled every {data?.interval ?? 30}s while the server runs; kept for 7 days.</span>
	</div>

	{#if samples.error}<LoadError error={samples.error} onretry={() => samples.load()} />{/if}

	<div class="grid gap-3 sm:grid-cols-3 sm:gap-4">
		{#each tiles as t (t.label)}<StatTile {...t} />{/each}
	</div>

	{#if !data}
		{#if !samples.error}<Loading class="card px-4 py-8 text-center" />{/if}
	{:else if !points.length}
		<p class="card px-4 py-8 text-center font-marker text-faint">
			{running ? 'first sample lands in about 30 seconds.' : 'no samples in this range. start the server to record some.'}
		</p>
	{:else}
		{#each charts as c (c.title)}
			<ChartCard title={c.title} info={c.info}>
				<StackChart
					lines={c.lines}
					since={samples.since}
					until={samples.until}
					bucket={samples.bucket}
					ticks={samples.ticks}
					ceil={c.ceil}
					format={c.format}
					bind:hover
					label="{c.title}, last {samples.rangeName}"
				/>
			</ChartCard>
		{/each}
	{/if}

	{#if data}
		<div class="flex items-center gap-1 pt-2">
			<h2 class="text-xl">Network</h2>
			<InfoTip
				label="About the network figures"
				text="The server's own summary of its connections, which it logs once a minute while players are on, with Log player activity on under Settings (ReSkate 1.1.5 or newer)."
			/>
		</div>
		{#if !net.length}
			<p class="card px-4 py-6 text-sm text-faint">
				No network figures in this range. The server logs them once a minute while players are on, with Log player activity on under
				<a class="text-accent hover:underline" href="/s/{ctx.id}/settings">Settings</a>.
			</p>
		{:else}
			{#each netCharts as c (c.title)}
				<ChartCard title={c.title} info={c.info}>
					<StackChart
						lines={c.lines}
						since={samples.since}
						until={samples.until}
						bucket={samples.bucket}
						ticks={samples.ticks}
						ceil={c.ceil}
						format={c.format}
						bind:hover
						label="{c.title}, last {samples.rangeName}"
					/>
				</ChartCard>
			{/each}
		{/if}
	{/if}

	<section class="card">
		<div class="border-b-2 border-line px-4 py-3"><h2>Runs</h2></div>
		{#if data?.runs.length}
			<div class="overflow-x-auto">
				<table class="table">
					<thead><tr><th>Started</th><th>Ran for</th><th>Avg CPU</th><th>Peak CPU</th><th>Avg memory</th><th>Peak memory</th><th>Peak players</th></tr></thead>
					<tbody>
						{#each data.runs as r (r.run)}
							<tr>
								<td class="whitespace-nowrap">
									{dateTime(r.run)}
									{#if current(r.run)}<span class="sticker sticker-accent ml-2">Now</span>{/if}
								</td>
								<td class="text-muted tabular-nums">{duration(r.run * 1000, r.last * 1000)}</td>
								<td class="tabular-nums">{percent(r.cpu)}</td>
								<td class="tabular-nums">{percent(r.cpuMax)}</td>
								<td class="tabular-nums">{memory(r.mem)}</td>
								<td class="tabular-nums">{memory(r.memMax)}</td>
								<td class="tabular-nums">{r.players}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{:else if data}
			<p class="px-4 py-6 text-sm text-faint">No runs in this range.</p>
		{/if}
	</section>
</div>
