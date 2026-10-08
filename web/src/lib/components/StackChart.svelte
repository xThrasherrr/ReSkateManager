<script lang="ts" module>
	export interface Series {
		label: string;
		color: string;
		points: { at: number; v: number; hi?: number; run?: number }[]; // at: unix seconds; run: the process, for a server's own line
		/** Lines only: how the tooltip names the faint band drawn from v up to hi. */
		band?: (v: number, hi: number) => string;
		dash?: boolean;
	}
</script>

<script lang="ts">
	// Parts of a whole stacked as areas (each server's share), with lines over
	// them (the machine's totals). Charts on a page share `hover` so their
	// crosshairs move together.
	let {
		layers = [],
		lines = [],
		since,
		until,
		bucket,
		ticks,
		ceil,
		top: fixedTop,
		format,
		hover = $bindable(null),
		label
	}: {
		layers?: Series[];
		lines?: Series[];
		since: number;
		until: number;
		bucket: number;
		ticks: { at: number; label: string }[];
		ceil: (v: number) => number; // a round axis top at or above v
		top?: number; // a fixed axis top instead, such as the machine's memory
		format: (v: number) => string;
		hover?: number | null;
		label: string; // what screen readers call the chart: its title and range
	} = $props();

	let w = $state(0);
	let svg: SVGSVGElement | undefined = $state();
	const h = 180;
	const pad = { l: 56, r: 12, t: 10, b: 22 };
	const iw = $derived(Math.max(1, w - pad.l - pad.r));
	const ih = h - pad.t - pad.b;

	// Every point in time any series has, and each layer's band in the stack
	// there; a layer without a point at some time adds nothing to it.
	const times = $derived([...new Set([...layers, ...lines].flatMap((s) => s.points.map((p) => p.at)))].sort((a, b) => a - b));
	const stacks = $derived.by(() => {
		const sum = new Map(times.map((t) => [t, 0]));
		return layers.map((s) => {
			const own = new Map(s.points.map((p) => [p.at, p.v]));
			const lo = new Map(sum);
			for (const t of times) sum.set(t, sum.get(t)! + (own.get(t) ?? 0));
			return { s, own, lo, hi: new Map(sum) };
		});
	});
	const top = $derived(
		fixedTop || ceil(Math.max(0, ...(stacks.at(-1) ? stacks.at(-1)!.hi.values() : []), ...lines.flatMap((s) => s.points.map((p) => p.hi ?? p.v))))
	);
	const x = (t: number) => pad.l + ((t - since) / (until - since)) * iw;
	const y = (v: number) => pad.t + ih - (Math.min(v, top) / top) * ih;

	// A gap of over two buckets (the manager was down) breaks lines and areas,
	// and a line breaks where the server's process started again.
	function split<T>(xs: T[], at: (x: T) => number, run: (x: T) => number | undefined = () => undefined): T[][] {
		const out: T[][] = [];
		let cur: T[] = [];
		for (const p of xs) {
			const last = cur.at(-1);
			if (last !== undefined && (at(p) - at(last) > bucket * 2 || run(p) !== run(last))) {
				out.push(cur);
				cur = [];
			}
			cur.push(p);
		}
		if (cur.length) out.push(cur);
		return out;
	}
	const spans = $derived(split(times, (t) => t).filter((s) => s.length > 1));
	const pt = (t: number, v: number) => `${x(t).toFixed(1)},${y(v).toFixed(1)}`;
	const area = (span: number[], lo: Map<number, number>, hi: Map<number, number>) =>
		'M' + span.map((t) => pt(t, hi.get(t)!)).join('L') + 'L' + [...span].reverse().map((t) => pt(t, lo.get(t)!)).join('L') + 'Z';
	type P = Series['points'][number];
	const path = (seg: P[], val: (p: P) => number) => 'M' + seg.map((p) => pt(p.at, val(p))).join('L');
	const band = (seg: P[]) => path(seg, (p) => p.hi ?? p.v) + 'L' + [...seg].reverse().map((p) => pt(p.at, p.v)).join('L') + 'Z';

	const near = $derived.by(() => {
		if (hover == null) return null;
		let best: number | null = null;
		for (const t of times) if (Math.abs(t - hover) <= bucket && (best == null || Math.abs(t - hover) < Math.abs(best - hover))) best = t;
		return best;
	});
	// What the tooltip lists at the hovered time: lines first, then the stack from the top down.
	const readout = $derived.by(() => {
		if (near == null) return [];
		const out: { label: string; color: string; text: string; line: boolean }[] = [];
		for (const s of lines) {
			const p = s.points.find((p) => p.at === near);
			if (!p) continue;
			const extra = s.band && p.hi != null && p.hi > p.v ? ` · ${s.band(p.v, p.hi)}` : '';
			out.push({ label: s.label, color: s.color, text: format(p.v) + extra, line: true });
		}
		for (const k of [...stacks].reverse()) {
			const v = k.own.get(near);
			if (v) out.push({ label: k.s.label, color: k.s.color, text: format(v), line: false });
		}
		return out;
	});

	function move(e: PointerEvent) {
		if (!svg) return;
		const px = e.clientX - svg.getBoundingClientRect().left;
		hover = since + ((px - pad.l) / iw) * (until - since);
	}

	const time = (t: number) => new Date(t * 1000).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
</script>

<div class="relative" bind:clientWidth={w}>
	{#if w}
		<svg bind:this={svg} width={w} height={h} class="block select-none" role="img" aria-label={label}>
			{#each [0, top / 2, top] as v (v)}
				<line x1={pad.l} x2={w - pad.r} y1={y(v)} y2={y(v)} class="stroke-line" stroke-width="1" />
				<text x={pad.l - 8} y={y(v)} dy="0.32em" text-anchor="end" class="fill-faint text-[10px] tabular-nums">{format(v)}</text>
			{/each}
			{#each ticks as t (t.at)}
				<text x={x(t.at)} y={h - 6} text-anchor="middle" class="fill-faint text-[10px]">{t.label}</text>
			{/each}
			{#each stacks as k, i (i)}
				{#each spans as span, j (j)}
					<path d={area(span, k.lo, k.hi)} fill={k.s.color} opacity="0.55" />
				{/each}
			{/each}
			{#each lines as s, i (i)}
				{#each split(s.points, (p) => p.at, (p) => p.run) as seg, j (j)}
					{#if seg.some((p) => p.hi != null && p.hi > p.v)}
						<path d={band(seg)} fill={s.color} opacity="0.14" />
					{/if}
					{#if seg.length > 1}
						<path
							d={path(seg, (p) => p.v)}
							fill="none"
							stroke={s.color}
							stroke-width="2"
							stroke-dasharray={s.dash ? '6 4' : undefined}
							stroke-linejoin="round"
							stroke-linecap="round"
						/>
					{:else if seg[0]}
						<circle cx={x(seg[0].at)} cy={y(seg[0].v)} r="3" fill={s.color} />
					{/if}
				{/each}
			{/each}
			{#if near != null}
				<line x1={x(near)} x2={x(near)} y1={pad.t} y2={pad.t + ih} class="stroke-line-strong" stroke-width="1" />
				{#each lines as s, i (i)}
					{@const p = s.points.find((p) => p.at === near)}
					{#if p && !s.dash}<circle cx={x(p.at)} cy={y(p.v)} r="4.5" fill={s.color} class="stroke-panel" stroke-width="2" />{/if}
				{/each}
			{/if}
			<rect
				x={pad.l}
				y={pad.t}
				width={iw}
				height={ih}
				fill="transparent"
				role="presentation"
				onpointermove={move}
				onpointerleave={() => (hover = null)}
			/>
		</svg>
		{#if near != null && readout.length}
			{@const left = x(near)}
			<div
				class="pointer-events-none absolute top-1 z-10 rounded-md border-2 border-ink bg-raised px-2 py-1 text-xs whitespace-nowrap shadow-ledge-sm"
				style:left="{left}px"
				style:translate={left > w / 2 ? 'calc(-100% - 10px) 0' : '10px 0'}
			>
				<div class="text-faint">{time(near)}</div>
				{#each readout as r, i (i)}
					<div class="flex items-center gap-1.5 tabular-nums">
						<span class="shrink-0 {r.line ? 'h-0.5 w-2.5' : 'size-2.5 rounded-sm'}" style:background={r.color}></span>
						<span class={r.line ? 'font-semibold text-fg' : 'text-muted'}>{r.label}</span>
						<span class="ml-auto pl-3 {r.line ? 'font-semibold text-fg' : 'text-fg'}">{r.text}</span>
					</div>
				{/each}
			</div>
		{/if}
	{/if}
</div>
{#if lines.length + layers.length > 1}
	<div class="flex flex-wrap gap-x-4 gap-y-1 px-2 pt-2 text-xs text-muted">
		{#each lines as s, i (i)}
			<span class="flex items-center gap-1.5">
				<svg width="14" height="4" aria-hidden="true"
					><line x1="1" x2="13" y1="2" y2="2" stroke={s.color} stroke-width="2" stroke-dasharray={s.dash ? '4 2' : undefined} stroke-linecap="round" /></svg
				>
				{s.label}
			</span>
		{/each}
		{#each layers as s, i (i)}
			<span class="flex items-center gap-1.5"><span class="size-2.5 rounded-sm opacity-80" style:background={s.color}></span>{s.label}</span>
		{/each}
	</div>
{/if}
