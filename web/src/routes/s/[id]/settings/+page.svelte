<script lang="ts">
	import { untrack } from 'svelte';
	import { api, ApiError, mapChoices, message, type Field, type ModList } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { session } from '#lib/session.svelte.js';
	import { toast } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import Modal from '#lib/components/Modal.svelte';
	import Icon from '#lib/components/Icon.svelte';
	import InfoTip from '#lib/components/InfoTip.svelte';
	import MapPool from '#lib/components/MapPool.svelte';
	import ColourInput from '#lib/components/ColourInput.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';
	import { control, utf8Length } from '#lib/text.js';

	interface Result {
		command: string;
		reply: string;
		error?: string;
	}

	const ctx = useLive();
	// Settings the server's layout no longer has, such as reserved slots since ReSkate 1.1.7.
	let gone = $state.raw<string[]>([]);
	const fields = $derived((session.meta?.settings ?? []).filter((f) => !gone.includes(f.key)));
	const groups = $derived([...new Set(fields.map((f) => f.group))]);
	const canEdit = $derived(ctx.can('settings.edit'));
	const status = $derived(ctx.live.state?.state);
	const running = $derived(status === 'running' || status === 'starting');

	// saved is the server's copy; values is the form, edited in place.
	let saved = $state.raw<Record<string, unknown>>({});
	let values = $state<Record<string, any>>({});
	// Settings the running server's build predates, such as the map pool on an older one.
	let unknown = $state.raw<string[]>([]);
	let error = $state('');
	let loaded = $state(false); // the form waits for the server's values, so no edit is lost to them
	let busy = $state(false);
	let results = $state.raw<Result[]>([]);
	let restartKeys = $state.raw<string[]>([]);
	let confirmRestart = $state(false);
	// Secret fields shown in plain text, by key.
	let reveal = $state<Record<string, boolean>>({});

	async function load() {
		error = '';
		try {
			const r = await api.get<{ values: Record<string, unknown>; unknown?: string[]; gone?: string[] }>(`/instances/${ctx.id}/settings`);
			saved = r.values;
			values = structuredClone(r.values);
			unknown = r.unknown ?? [];
			gone = r.gone ?? [];
			loaded = true;
		} catch (e) {
			error = message(e);
		}
	}
	$effect(() => {
		ctx.id;
		untrack(load);
	});
	// Which settings the server's build knows is only told once it runs.
	const beginUnknown = newest();
	$effect(() => {
		if (status !== 'running') return;
		const current = beginUnknown();
		untrack(() => api.get<{ unknown?: string[]; gone?: string[] }>(`/instances/${ctx.id}/settings`)).then(
			(r) => {
				if (!current()) return;
				unknown = r.unknown ?? [];
				gone = r.gone ?? []; // a newer server lays its file out again on its first start
			},
			() => {}
		);
	});

	// The map field's choices: retail maps and every map in the server's Mods folder.
	let modList = $state.raw<ModList | null>(null);
	const beginMods = newest();
	$effect(() => {
		status; // a running server also reports which maps it loaded
		const current = beginMods();
		untrack(() => api.get<ModList>(`/instances/${ctx.id}/mods`)).then(
			(m) => current() && (modList = m),
			() => current() && (modList = null)
		);
	});
	const maps = $derived(modList ? mapChoices(modList) : null);
	// A saved map the list doesn't have (a level path, or a mod since removed) stays selectable.
	const unlisted = $derived.by(() => {
		const v = values.map as string | undefined;
		if (!maps || !v) return '';
		const all = [...maps.retail, ...maps.removed, ...maps.mods.flatMap((m) => m.maps.map((x) => x.name))];
		return all.includes(v) ? '' : v;
	});

	const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
	const dirty = $derived(fields.filter((f) => !same(values[f.key], saved[f.key])).map((f) => f.key));

	async function save(restart = false) {
		busy = true;
		const changed: Record<string, unknown> = {};
		for (const k of dirty) changed[k] = values[k];
		try {
			const r = await api.post<{ results: Result[]; restarted?: boolean; notApplied?: string[]; reply?: string }>(
				`/instances/${ctx.id}/settings`,
				{ values: changed, restart }
			);
			results = r.results ?? [];
			if (r.reply) toast.ok(r.reply);
			else if (r.notApplied?.length) toast.error(`The server did not accept: ${r.notApplied.join(', ')}.`);
			else toast.ok(r.restarted ? 'Saved; the server restarted to apply it.' : 'Saved and applied live.');
			await load();
		} catch (e) {
			if (e instanceof ApiError && e.status === 409 && Array.isArray(e.body.restartKeys)) {
				restartKeys = e.body.restartKeys as string[];
				confirmRestart = true;
			} else toast.error(e);
		} finally {
			busy = false;
		}
	}

	function label(key: string) {
		return fields.find((f) => f.key === key)?.label ?? key;
	}

	function setNum(f: Field, v: string) {
		values[f.key] = v === '' ? '' : Number(v);
	}

	guardUnsaved(() => canEdit && dirty.length > 0);

	// What is wrong with a changed value by the rules every setting shares, as
	// the manager checks them: rules of one setting alone it says on saving.
	function problem(f: Field, v: unknown): string {
		if (f.type === 'int' || f.type === 'number') {
			const n = typeof v === 'number' ? v : NaN;
			const min = f.min ?? 0; // as the manager reads an unset minimum
			if (!Number.isFinite(n)) return 'Enter a number.';
			if (f.type === 'int' && !Number.isInteger(n)) return 'Enter a whole number.';
			if (n < min || (f.max && n > f.max)) return f.max ? `Pick ${min} to ${f.max}.` : `Pick ${min} or more.`;
		}
		if (f.type === 'string' && typeof v === 'string') {
			if (control.test(v)) return "It can't hold control characters.";
			if (f.maxLen && utf8Length(v) > f.maxLen) return `At most ${f.maxLen} bytes.`;
		}
		if (f.type === 'color' && !colour.test(String(v ?? ''))) return 'Enter a colour like #8E5CFF.';
		return '';
	}
	const colour = /^#?[0-9a-f]{6}$/i;
	// The server's chat colours as players will see them, or the saved ones while one is half typed.
	const shown = (key: string) => {
		const v = String(values[key] ?? '');
		return colour.test(v) ? `#${v.replace('#', '')}` : String(saved[key] ?? '');
	};
	const problems = $derived(Object.fromEntries(fields.filter((f) => dirty.includes(f.key)).map((f) => [f.key, problem(f, values[f.key])])));
	const blocked = $derived(Object.values(problems).some(Boolean));
</script>

{#if !loaded}
	<div class="p-4 sm:p-6">
		{#if error}<LoadError {error} onretry={load} />{:else}<Loading />{/if}
	</div>
{:else}
	<div class="mx-auto max-w-4xl space-y-4 p-4 sm:space-y-6 sm:p-6">
		{#if error}<LoadError {error} onretry={load} />{/if}
		<p class="text-sm text-muted">
			{#if running}
				Changes apply live through the server console. Settings marked <span class="sticker sticker-warn mx-0.5">restart</span> need a restart.
			{:else}
				The server is stopped; changes are written to ReSkateServer.json and apply when it starts.
			{/if}
		</p>
		{#each groups as group (group)}
			<section class="card">
				<h2 class="flex items-center gap-1.5 border-b-2 border-line px-4 py-3">
					{group}
					{#if session.meta?.groupInfo?.[group]}<InfoTip text={session.meta.groupInfo[group]} label="About {group}" />{/if}
				</h2>
				<div class="divide-y divide-line/60">
					{#each fields.filter((f) => f.group === group) as f (f.key)}
						{@const changed = dirty.includes(f.key)}
						{@const newer = unknown.includes(f.key)}
						{@const locked = !canEdit || newer}
						<div class="grid items-center gap-2 px-4 py-3 sm:grid-cols-[1fr_minmax(0,18rem)]">
							<div>
								<div class="flex items-center gap-1">
									<label for={f.key} class="text-sm font-medium {changed ? 'text-accent' : ''}">
										{f.label}
										{#if f.restart}<span class="sticker sticker-warn ml-1.5">restart</span>{/if}
										{#if f.owner}<span class="sticker sticker-info ml-1.5" title="Only owners see or change this.">owners only</span>{/if}
										{#if newer}<span class="sticker sticker-pink ml-1.5" title="The server's ReSkate build is older than this setting. Update it from the Updates tab.">update the server</span>{/if}
									</label>
									<!-- Outside the label: a click inside one would also toggle its input. -->
									{#if f.info}<InfoTip text={f.info} label="About {f.label}" />{/if}
								</div>
								{#if f.help}<p class="text-xs text-faint">{f.help}</p>{/if}
							</div>
							<div class={f.type === 'maps' || f.long ? 'sm:col-span-2' : ''}>
								{#if f.type === 'bool'}
									<label class="inline-flex cursor-pointer items-center gap-2">
										<input id={f.key} type="checkbox" class="peer sr-only" disabled={locked} bind:checked={values[f.key]} />
										<span class="switch"></span>
										<span class="text-sm text-muted">{values[f.key] ? 'On' : 'Off'}</span>
									</label>
								{:else if f.type === 'maps'}
									<MapPool
										id={f.key}
										bind:value={values[f.key]}
										choices={maps}
										current={running ? (ctx.live.state?.info.map ?? '') : ''}
										map={saved.map as string | undefined}
										disabled={locked}
									/>
								{:else if f.type === 'enum'}
									<select id={f.key} class="input" disabled={locked} bind:value={values[f.key]}>
										{#each f.options ?? [] as o (o)}<option value={o}>{o}</option>{/each}
									</select>
								{:else if f.type === 'int' || f.type === 'number'}
									<input
										id={f.key}
										class="input tabular-nums"
										type="number"
										min={f.min ?? 0}
										max={f.max || undefined}
										step={f.type === 'int' ? 1 : 'any'}
										disabled={locked}
										value={values[f.key]}
										oninput={(e) => setNum(f, e.currentTarget.value)}
									/>
								{:else if f.type === 'color'}
									<ColourInput id={f.key} bind:value={values[f.key]} disabled={locked} />
								{:else if f.type === 'list'}
									<textarea
										id={f.key}
										class="input h-20 py-1.5 font-mono text-xs"
										disabled={locked}
										placeholder="One per line"
										value={((values[f.key] as string[]) ?? []).join('\n')}
										oninput={(e) =>
											(values[f.key] = e.currentTarget.value
												.split(/\s+/)
												.map((s) => s.trim())
												.filter(Boolean))}
									></textarea>
								{:else if f.secret}
									<div class="flex gap-1">
										<!-- new-password: browsers fill "off" password fields anyway, with the panel's own password. -->
										<input
											id={f.key}
											class="input"
											type={reveal[f.key] ? 'text' : 'password'}
											maxlength={f.maxLen}
											disabled={locked}
											bind:value={values[f.key]}
											autocomplete="new-password"
											data-1p-ignore
											data-lpignore="true"
										/>
										<button
											type="button"
											class="btn btn-ghost px-2"
											onclick={() => (reveal[f.key] = !reveal[f.key])}
											title={reveal[f.key] ? 'Hide' : 'Show'}
											aria-label={reveal[f.key] ? `Hide ${f.label}` : `Show ${f.label}`}
											aria-pressed={!!reveal[f.key]}
										>
											<Icon name="key" size={14} />
										</button>
									</div>
								{:else if f.key === 'map' && maps}
									<select id={f.key} class="input" disabled={locked} bind:value={values[f.key]}>
										{#if unlisted}<option value={unlisted}>{unlisted} (not in Mods)</option>{/if}
										<optgroup label="Retail">
											{#each maps.retail as m (m)}<option value={m}>{m}</option>{/each}
										</optgroup>
										{#each maps.mods as mod (mod.folder)}
											<optgroup label={mod.title}>
												{#each mod.maps as m, i (i)}
													<!-- The server only knows maps from mods it loaded at start. -->
													<option value={m.name} disabled={m.pending}>{m.name}{m.pending ? ' (restart to load)' : ''}</option>
												{/each}
											</optgroup>
										{/each}
										{#if maps.removed.length}
											<optgroup label="Removed from Mods (until restart)">
												{#each maps.removed as m (m)}<option value={m}>{m}</option>{/each}
											</optgroup>
										{/if}
									</select>
									<a class="mt-1 inline-block text-xs text-accent hover:underline" href="/s/{ctx.id}/mods">View map mods</a>
								{:else if f.long}
									<!-- The server takes it as one chat line, so line breaks become spaces. Its limit is in bytes. -->
									{@const used = new TextEncoder().encode(String(values[f.key] ?? '')).length}
									<textarea
										id={f.key}
										class="input h-auto min-h-16 resize-y py-1.5 leading-snug"
										rows="3"
										disabled={locked}
										value={values[f.key] as string}
										oninput={(e) => (values[f.key] = e.currentTarget.value.replace(/[\r\n]+/g, ' '))}
										onkeydown={(e) => e.key === 'Enter' && e.preventDefault()}
									></textarea>
									{#if f.maxLen}
										<p class="text-right text-xs tabular-nums {used > f.maxLen ? 'text-bad' : 'text-faint'}">{used}/{f.maxLen} bytes</p>
									{/if}
								{:else}
									<input id={f.key} class="input" disabled={locked} bind:value={values[f.key]} />
								{/if}
								{#if problems[f.key]}<p class="mt-1 text-xs text-bad">{problems[f.key]}</p>{/if}
							</div>
							{#if f.key === 'chat_text_color'}
								<!-- As the game draws a server line: a filled badge, the name in its colour, then the text. -->
								<p class="rounded-lg bg-ink px-2.5 py-1.5 text-sm leading-relaxed break-words sm:col-span-2" title="Preview">
									<span class="mr-1 rounded px-1.5 py-px text-[11px] font-semibold text-[#121216]" style:background-color={shown('chat_color')}>Server</span>
									<span style:color={shown('chat_color')}>{values.name || 'ReSkate server'}:</span>
									<span style:color={shown('chat_text_color')}>{values.welcome || 'Welcome to the server!'}</span>
								</p>
							{/if}
						</div>
					{/each}
				</div>
			</section>
		{/each}

		{#if results.length}
			<section class="card">
				<h2 class="border-b-2 border-line px-4 py-3">Last save</h2>
				<div class="space-y-1 px-4 py-3 font-mono text-xs">
					{#each results as r, i (i)}
						<div><span class="text-accent">&gt; {r.command}</span> <span class={r.error ? 'text-bad' : 'text-muted'}>{r.error ?? r.reply}</span></div>
					{/each}
				</div>
			</section>
		{/if}

		{#if canEdit && dirty.length}
			<div class="sticky bottom-4 z-40 flex animate-pop items-center gap-2 rounded-xl border-2 border-ink bg-warn px-3 py-2.5 text-ink shadow-sticker-lg sm:bottom-6 sm:gap-3 sm:px-4 sm:py-3">
				<span class="font-display text-base leading-tight sm:text-lg">{dirty.length} unsaved change{dirty.length === 1 ? '' : 's'}</span>
				<button class="btn ml-auto" onclick={() => (values = structuredClone(saved))}>Discard</button>
				<button class="btn btn-primary" disabled={busy || blocked} title={blocked ? 'Fix the settings marked in red first' : undefined} onclick={() => save(false)}>{busy ? 'Saving…' : 'Save'}</button>
			</div>
		{/if}
	</div>
{/if}

<Modal bind:open={confirmRestart} title="Restart the server?">
	<p class="text-sm text-muted">These settings only apply after a restart:</p>
	<ul class="mt-2 list-inside list-disc text-sm">
		{#each restartKeys as k (k)}<li>{label(k)}</li>{/each}
	</ul>
	<p class="mt-3 text-sm text-muted">Everyone on the server is disconnected and can rejoin once it is back up.</p>
	{#snippet actions()}
		<button class="btn" onclick={() => (confirmRestart = false)}>Cancel</button>
		<button
			class="btn btn-primary"
			onclick={() => {
				confirmRestart = false;
				save(true);
			}}>Save and restart</button
		>
	{/snippet}
</Modal>
