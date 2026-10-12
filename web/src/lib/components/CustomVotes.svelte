<script lang="ts">
	import { blankVote, voteNumbers, voteProblems, type CustomVote } from '#lib/votes.js';
	import Icon from './Icon.svelte';
	import RowList from './RowList.svelte';

	// The owner's own votes (ReSkate 2.0.2): each runs a server command when
	// it passes. A vote that is new, gone or rewritten applies on restart, as
	// the server has no command for that; its switch and limits apply live.
	let {
		id,
		value = $bindable(),
		max,
		saved = [],
		disabled = false
	}: { id: string; value: CustomVote[] | undefined; max?: number; saved?: CustomVote[]; disabled?: boolean } = $props();

	const problems = $derived(voteProblems(value ?? []));

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
</script>

<RowList
	{id}
	bind:value
	{max}
	blank={blankVote}
	{problems}
	empty="No custom votes."
	addLabel="Add a vote"
	{examples}
	exampleLabel={(e) => `/vote ${e.name}${e.choices.length ? ` <${e.choices.join('|')}>` : ''}`}
	exampleTitle={(e) => `${e.command}: ${e.description}`}
	{disabled}
>
	{#snippet row(v, i, set, remove)}
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
				oninput={(e) => set({ name: e.currentTarget.value.toLowerCase() })}
			/>
			{#if v.choices.length}<span class="font-mono text-sm text-faint">&lt;{v.choices.join('|')}&gt;</span>{/if}
			{#if restarts(v, i)}<span class="sticker sticker-warn" title="A new or edited vote applies when the server restarts.">restart</span>{/if}
			<div class="ml-auto flex items-center gap-2">
				<label class="inline-flex cursor-pointer items-center gap-2">
					<input type="checkbox" class="peer sr-only" {disabled} checked={v.enabled} onchange={(e) => set({ enabled: e.currentTarget.checked })} />
					<span class="switch"></span>
					<span class="text-sm text-muted">{v.enabled ? 'On' : 'Off'}</span>
				</label>
				<button type="button" class="btn btn-ghost btn-sm" {disabled} aria-label="Remove /vote {v.name || 'this vote'}" title="Remove" onclick={remove}>
					<Icon name="trash" size={14} />
				</button>
			</div>
		</div>
		<div class="grid gap-2 sm:grid-cols-2">
			<label class="block">
				<span class="label">Description</span>
				<input class="input" placeholder="What it does, for /vote list" {disabled} value={v.description} oninput={(e) => set({ description: e.currentTarget.value })} />
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
					oninput={(e) => set({ command: e.currentTarget.value })}
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
						oninput={(e) => set({ choices: choices(e.currentTarget.value) })}
					/>
				</label>
			{/if}
		</div>
		<div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
			{#each voteNumbers as n (n.key)}
				<label class="block">
					<span class="label">{n.label}</span>
					<input
						class="input tabular-nums"
						type="number"
						min={n.min}
						max={n.max}
						step="1"
						{disabled}
						value={v[n.key]}
						oninput={(e) => set({ [n.key]: num(e.currentTarget.value) })}
					/>
				</label>
			{/each}
		</div>
	{/snippet}
	{#snippet note()}
		<p class="text-xs text-faint">A length or cooldown of 0 takes the one on the Votes card.</p>
	{/snippet}
</RowList>
