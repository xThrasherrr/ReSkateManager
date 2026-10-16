<script lang="ts">
	import { onMount } from 'svelte';
	import { api, message, sleep, type Plugin, type PluginsInfo, type PluginState } from '#lib/api.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { bytes, duration, plural } from '#lib/format.js';
	import FolderPath from './FolderPath.svelte';
	import Icon from './Icon.svelte';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';

	let info = $state.raw<PluginsInfo | null>(null);
	let loadError = $state('');
	let busy = $state<string | null>(null); // the plugin an action waits on

	async function load() {
		try {
			info = await api.get<PluginsInfo>('/manager/plugins');
			loadError = '';
		} catch (e) {
			loadError = message(e);
		}
	}
	onMount(() => {
		load();
		// Running plugins log and count as they go.
		const t = setInterval(() => {
			if (!document.hidden && !busy) load();
		}, 5000);
		return () => clearInterval(t);
	});

	// After it's turned on or reloaded, until it's running or has failed.
	async function settle(id: string) {
		for (let i = 0; i < 40; i++) {
			await load();
			if (info?.plugins.find((p) => p.id === id)?.state !== 'starting') return;
			await sleep(250);
		}
	}

	async function toggle(p: Plugin, on: boolean) {
		busy = p.id;
		const done = await attempt(() => api.patch<Plugin>(`/manager/plugins/${encodeURIComponent(p.id)}`, { enabled: on }), on ? `Turned on ${p.name}` : `Turned off ${p.name}`);
		if (done) await settle(p.id);
		else await load();
		busy = null;
	}

	async function grant(p: Plugin, key: string, on: boolean) {
		busy = p.id;
		const granted = on ? [...p.granted, key] : p.granted.filter((k) => k !== key);
		const label = info?.grants.find((g) => g.key === key)?.label.toLowerCase() ?? key;
		await attempt(() => api.patch<Plugin>(`/manager/plugins/${encodeURIComponent(p.id)}`, { granted }), on ? `${p.name} may now ${label}` : `${p.name} may no longer ${label}`);
		await load();
		busy = null;
	}

	async function reload(p: Plugin) {
		busy = p.id;
		if (await attempt(() => api.post<Plugin>(`/manager/plugins/${encodeURIComponent(p.id)}/reload`), `Reloaded ${p.name}`)) await settle(p.id);
		busy = null;
	}

	const sticker: Record<PluginState, [string, string]> = {
		off: ['', 'Off'],
		starting: ['sticker-info', 'Starting'],
		running: ['sticker-accent', 'Running'],
		failed: ['sticker-pink', 'Failed'],
		stopped: ['sticker-warn', 'Stopped']
	};
	const levelColor = { info: 'text-muted', warn: 'text-warn', error: 'text-bad' };
	const time = (ms: number) => new Date(ms).toLocaleTimeString();
	const ms = (v: number) => (v < 0.1 ? '<0.1' : v < 10 ? v.toFixed(1) : String(Math.round(v)));
</script>

{#if !info}
	{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
{:else}
	<div class="space-y-4">
		{#if loadError}<LoadError error={loadError} onretry={load} />{/if}
		<p class="text-sm text-muted">
			Plugins are Lua 5.4 scripts that run inside the manager and act on what happens on your servers: players joining and leaving, chat, throwdowns, map
			and state changes. A plugin runs code on this machine, as a mod does, so only turn on plugins you trust.
		</p>
		<p class="flex items-start gap-2 text-sm">
			<span class="sticker sticker-warn shrink-0">Preview</span>
			<span class="text-muted">Plugin API {info.api} may still change, so a plugin made for it may need changes later.</span>
		</p>
		<FolderPath label="Plugins folder" folder={info.dir} />
		<p class="text-xs text-faint">
			Each plugin is a folder there with a <code>plugin.toml</code> and a <code>main.lua</code>. A call into a plugin may run for {info.callMs} ms, and a plugin may use
			{info.memoryMB} MB. One that keeps failing or running over is stopped. Its log is on its card and in <code>data/plugins.log</code>.
		</p>
		<details class="rounded-xl border-2 border-line px-3 py-2 text-sm">
			<summary class="cursor-pointer font-semibold">Make one</summary>
			<div class="mt-2 space-y-2 text-xs">
				<p class="text-muted">A folder <code>welcome</code> in the plugins folder, with:</p>
				<pre class="overflow-x-auto rounded-lg bg-sunken p-2 font-mono"><span class="text-faint"># plugin.toml</span>
name = "Welcome"
version = "1.0.0"
api = {info.api}
permissions = ["players.chat"]</pre>
				<pre class="overflow-x-auto rounded-lg bg-sunken p-2 font-mono"><span class="text-faint">-- main.lua</span>
on("player.joined", function(e)
  log(e.name .. " joined " .. e.server)
  server.msg(e.server, e.id, "Welcome, " .. e.name)
end)</pre>
				<p class="text-muted">
					Then turn it on below, and tick what it asks to do. <code>on(event, fn)</code> takes {plural(info.events.length, 'event')}:
					{#each info.events as e, i}<code>{e}</code>{i < info.events.length - 1 ? ', ' : ''}{/each}. <code>after(seconds, fn)</code> and
					<code>every(seconds, fn)</code> run it later, <code>cancel(timer)</code> stops one, <code>log(...)</code> writes to its log, and
					<code>require("lib.util")</code> runs <code>lib/util.lua</code> from its folder. <code>server.players(id)</code> and
					<code>server.info(id)</code> read a server; <code>server.say</code>, <code>server.msg</code>, <code>server.kick</code> and the rest act on one, a few a
					second, with what you grant the plugin.
				</p>
			</div>
		</details>

		{#if info.plugins.length === 0}
			<p class="rounded-xl border-2 border-dashed border-line px-4 py-6 text-center text-sm text-muted">No plugins in the folder yet.</p>
		{:else}
			<ul class="space-y-3">
				{#each info.plugins as p (p.id)}
					{@const [color, label] = sticker[p.state]}
					<li class="space-y-2 rounded-xl border-2 border-line p-3">
						<div class="flex flex-wrap items-center gap-x-3 gap-y-2">
							<div class="min-w-0 flex-1">
								<div class="flex flex-wrap items-center gap-2">
									<span class="truncate font-semibold">{p.name}</span>
									{#if p.version}<span class="text-xs text-faint">{p.version}</span>{/if}
									<span class="sticker {color}">{label}</span>
								</div>
								{#if p.name !== p.id}<div class="truncate font-mono text-xs text-faint">{p.id}</div>{/if}
							</div>
							<div class="flex items-center gap-2">
								<button class="btn btn-ghost btn-sm" disabled={busy !== null} title="Read its files again and start it afresh" onclick={() => reload(p)}>
									<Icon name="restart" size={14} /> Reload
								</button>
								<label class="inline-flex cursor-pointer items-center gap-2" title={p.enabled ? 'Turn it off' : 'Turn it on'}>
									<input
										type="checkbox"
										class="peer sr-only"
										checked={p.enabled}
										disabled={busy !== null}
										aria-label="{p.name} on"
										onchange={(e) => toggle(p, e.currentTarget.checked)}
									/>
									<span class="switch"></span>
								</label>
							</div>
						</div>
						{#if p.description}<p class="text-sm text-muted">{p.description}</p>{/if}
						{#if p.problem}
							<p class="flex items-start gap-1.5 text-sm text-bad"><Icon name="alert" size={14} class="mt-0.5 shrink-0" /> {p.problem}</p>
						{:else if p.reason && (p.state === 'failed' || p.state === 'stopped')}
							<p class="flex items-start gap-1.5 text-sm text-warn"><Icon name="alert" size={14} class="mt-0.5 shrink-0" /> <span class="min-w-0 break-words">{p.reason}</span></p>
						{/if}
						{#if p.unknown?.length}
							<p class="text-xs text-faint">plugin.toml has settings that do nothing: {p.unknown.join(', ')}</p>
						{/if}
						<p class="text-xs text-muted">
							{p.servers?.length ? `On ${p.servers.join(', ')}` : 'On every server'}{p.stats.handlers?.length ? ` · listens to ${p.stats.handlers.join(', ')}` : ''}
						</p>
						{#if p.permissions?.length}
							<fieldset class="space-y-1.5">
								<legend class="text-xs font-semibold text-muted">It asks to</legend>
								{#each p.permissions as key (key)}
									{@const g = info.grants.find((g) => g.key === key)}
									{#if g}
										<label class="flex items-start gap-2 text-sm">
											<input type="checkbox" class="mt-0.5" checked={p.granted.includes(key)} disabled={busy !== null} onchange={(e) => grant(p, key, e.currentTarget.checked)} />
											<span class="min-w-0">{g.label}<span class="block text-xs break-words text-faint">{g.calls.join(', ')}</span></span>
										</label>
									{:else}
										<p class="flex items-start gap-2 text-sm text-faint">
											<Icon name="alert" size={14} class="mt-0.5 shrink-0" />
											<span class="min-w-0 break-words"><code>{key}</code>: plugins can't be given this</span>
										</p>
									{/if}
								{/each}
							</fieldset>
						{/if}
						{#if p.state === 'running' || p.stats.calls > 0}
							<p class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-faint tabular-nums">
								{#if p.state === 'running' && p.since}<span>Up {duration(p.since)}</span>{/if}
								<span>{plural(p.stats.events, 'event')}</span>
								{#if p.stats.dropped}<span class="text-warn" title="Events and timers it was too busy for">{p.stats.dropped} dropped</span>{/if}
								{#if p.stats.errors}<span class="text-bad">{plural(p.stats.errors, 'error')}</span>{/if}
								<span title="Its slowest call">slowest {ms(p.stats.slowestMs)} ms</span>
								{#if p.stats.memory}<span>{bytes(p.stats.memory)}</span>{/if}
								{#if p.stats.timers}<span>{plural(p.stats.timers, 'timer')}</span>{/if}
								{#if p.stats.sent}<span title="Commands it gave its servers">{plural(p.stats.sent, 'command')}</span>{/if}
								{#if p.stats.unsent}<span class="text-warn" title="Commands not sent: too many were waiting">{p.stats.unsent} unsent</span>{/if}
								{#if p.restarts}<span class="text-warn" title="Fresh starts after running over its time">{plural(p.restarts, 'restart')}</span>{/if}
							</p>
						{/if}
						{#if p.recent.length}
							<details>
								<summary class="cursor-pointer text-xs font-semibold text-muted">Log ({p.recent.length})</summary>
								<ol class="mt-2 max-h-64 space-y-0.5 overflow-auto rounded-lg bg-sunken p-2 font-mono text-xs">
									{#each p.recent.toReversed() as l, i (i)}
										<li class="flex gap-2">
											<span class="shrink-0 text-faint">{time(l.at)}</span>
											<span class="min-w-0 break-words whitespace-pre-wrap {levelColor[l.level]}">{l.text}</span>
										</li>
									{/each}
								</ol>
							</details>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</div>
{/if}
