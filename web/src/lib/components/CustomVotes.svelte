<script lang="ts">
	import { blankVote, maxVotes, voteProblems, type CustomVote } from '#lib/votes.js';
	import Icon from './Icon.svelte';

	// The owner's own votes (ReSkate 2.0.2): each runs a server command when
	// it passes. A vote that is new, gone or rewritten applies on restart, as
	// the server has no command for that; its switch and limits apply live.
	let {
		id,
		value = $bindable(),
		saved = [],
		disabled = false
	}: { id: string; value: CustomVote[] | undefined; saved?: CustomVote[]; disabled?: boolean } = $props();

	const list = $derived(value ?? []);
	const problems = $derived(voteProblems(list));

	function set(i: number, change: Partial<CustomVote>) {
		value = list.map((v, j) => (j === i ? { ...v, ...change } : v));
	}
	const num = (s: string) => (s === '' ? NaN : Number(s));
	// Typed with spaces or commas; the server takes them lower case.
	const choices = (s: string) =>
		s
			.toLowerCase()
			.split(/[\s,|]+/)
			.filter(Boolean);

	// What only a restart changes, against the saved vote in the same place.
	const restarts = (v: CustomVote, i: number) => {
		const s = saved[i];
		return !s || s.name !== v.name.trim() || s.description !== v.description.trim() || s.command !== v.command.trim() || s.choices.join(' ') !== v.choices.join(' ');
	};

	// From the server's README: a reload of the map, and noclip by a vote.
	const examples: CustomVote[] = [
		{ ...blankVote(), name: 'restart', description: 'Reload the current map', command: 'map {map}' },
		{ ...blankVote(), name: 'noclip', description: 'Turn noclip on or off', command: 'noclip {arg}', choices: ['on', 'off'] }
	];
	const unused = $derived(examples.filter((e) => !list.some((v) => v.name.trim() === e.name)));
	const add = (v: CustomVote) => (value = [...list, structuredClone(v)]);
</script>

<div {id} class="space-y-2">
	{#if list.length}
		<ol class="space-y-2">
			{#each list as v, i (i)}
				<li class="space-y-2 rounded-lg border-2 p-3 {problems[i] ? 'border-bad/70' : 'border-line'}">
					<div class="flex flex-wrap items-center gap-2">
						<span class="font-mono text-sm text-muted">/vote</span>
						<input
							class="input w-36 font-mono"
							placeholder="name"
							maxlength="16"
							spellcheck="false"
							autocomplete="off"
							aria-label="Name players type"
							{disabled}
							value={v.name}
							oninput={(e) => set(i, { name: e.currentTarget.value.toLowerCase() })}
						/>
						{#if v.choices.length}<span class="font-mono text-sm text-faint">&lt;{v.choices.join('|')}&gt;</span>{/if}
						{#if restarts(v, i)}<span class="sticker sticker-warn" title="A new or edited vote applies when the server restarts.">restart</span>{/if}
						<div class="ml-auto flex items-center gap-2">
							<label class="inline-flex cursor-pointer items-center gap-2">
								<input type="checkbox" class="peer sr-only" {disabled} checked={v.enabled} onchange={(e) => set(i, { enabled: e.currentTarget.checked })} />
								<span class="switch"></span>
								<span class="text-sm text-muted">{v.enabled ? 'On' : 'Off'}</span>
							</label>
							<button
								type="button"
								class="btn btn-ghost btn-sm"
								{disabled}
								aria-label="Remove /vote {v.name || 'this vote'}"
								title="Remove"
								onclick={() => (value = list.filter((_, j) => j !== i))}
							>
								<Icon name="trash" size={14} />
							</button>
						</div>
					</div>
					<div class="grid gap-2 sm:grid-cols-2">
						<label class="block">
							<span class="label">Description</span>
							<input
								class="input"
								placeholder="What it does, for /vote list"
								{disabled}
								value={v.description}
								oninput={(e) => set(i, { description: e.currentTarget.value.replace(/[\r\n]+/g, ' ') })}
							/>
						</label>
						<label class="block">
							<span class="label">Command</span>
							<input
								class="input font-mono"
								placeholder={'map {map}'}
								spellcheck="false"
								autocomplete="off"
								{disabled}
								value={v.command}
								oninput={(e) => set(i, { command: e.currentTarget.value.replace(/[\r\n]+/g, ' ') })}
							/>
						</label>
						<!-- Only a command with {arg} takes a choice. -->
						{#if v.command.includes('{arg}') || v.choices.length}
							<label class="block sm:col-span-2">
								<span class="label">Choices</span>
								<input
									class="input font-mono"
									placeholder="on off"
									spellcheck="false"
									autocomplete="off"
									{disabled}
									value={v.choices.join(' ')}
									oninput={(e) => set(i, { choices: choices(e.currentTarget.value) })}
								/>
							</label>
						{/if}
					</div>
					<div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
						{#each [{ key: 'percent', label: '% to pass', min: 1, max: 100 }, { key: 'seconds', label: 'Length (s)', min: 0, max: 300 }, { key: 'cooldown_seconds', label: 'Cooldown (s)', min: 0, max: 3600 }, { key: 'min_players', label: 'Players on', min: 1, max: 249 }] as n (n.key)}
							<label class="block">
								<span class="label">{n.label}</span>
								<input
									class="input tabular-nums"
									type="number"
									min={n.min}
									max={n.max}
									step="1"
									{disabled}
									value={v[n.key as keyof CustomVote] as number}
									oninput={(e) => set(i, { [n.key]: num(e.currentTarget.value) })}
								/>
							</label>
						{/each}
					</div>
					{#if problems[i]}<p class="text-xs text-bad">{problems[i]}</p>{/if}
				</li>
			{/each}
		</ol>
		<p class="text-xs text-faint">A length or cooldown of 0 takes the one on the Votes card.</p>
	{:else}
		<p class="text-sm text-faint">No custom votes.</p>
	{/if}
	<div class="flex flex-wrap items-center gap-2">
		<button type="button" class="btn btn-sm" disabled={disabled || list.length >= maxVotes} onclick={() => add(blankVote())}>
			<Icon name="plus" size={14} />Add a vote
		</button>
		{#each unused as e (e.name)}
			<button type="button" class="btn btn-ghost btn-sm" disabled={disabled || list.length >= maxVotes} title={`${e.command}: ${e.description}`} onclick={() => add(e)}>
				<Icon name="plus" size={14} />/vote {e.name}{e.choices.length ? ` <${e.choices.join('|')}>` : ''}
			</button>
		{/each}
		<span class="ml-auto text-xs text-faint tabular-nums">{list.length}/{maxVotes}</span>
	</div>
</div>
