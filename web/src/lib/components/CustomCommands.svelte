<script lang="ts">
	import { blankCommand, commandProblems, maxRuns, pluginOf, type CustomCommand } from '#lib/commands.js';
	import Icon from './Icon.svelte';
	import LinesInput from './LinesInput.svelte';
	import RowList from './RowList.svelte';

	// The owner's own chat commands (ReSkate 2.0.3): each replies to whoever
	// typed it, runs server commands as the console, or both. The server reads
	// them only when it starts.
	let {
		id,
		value = $bindable(),
		max,
		disabled = false
	}: { id: string; value: CustomCommand[] | undefined; max?: number; disabled?: boolean } = $props();

	const problems = $derived(commandProblems(value ?? []));

	// From the server's README.
	const examples: CustomCommand[] = [
		{ ...blankCommand(), name: 'discord', reply: 'Join us: discord.gg/…' },
		{ ...blankCommand(), name: 'rules', command: ['announce-to {player} No griefing!'] },
		{ ...blankCommand(), name: 'restart', command: ['map {map}'], admin: true }
	];
</script>

<RowList
	{id}
	bind:value
	{max}
	blank={blankCommand}
	{problems}
	empty="No chat commands."
	addLabel="Add a command"
	{examples}
	exampleLabel={(e) => `/${e.name}`}
	exampleTitle={(e) => e.reply || e.command.join('; ')}
	{disabled}
>
	{#snippet row(c, i, set, remove)}
		{@const plugin = pluginOf(c)}
		{#if plugin}
			<!-- Put in as the server starts, for the plugins that are on. -->
			<p class="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
				<span class="font-mono">/{c.name}</span>
				<span class="sticker sticker-info">Plugin</span>
				{#if plugin === 'manager:parks'}
					<span class="text-muted">The manager answers it, for the players the <a class="text-accent hover:underline" href="parks">Parks</a> page lets spawn parks.</span>
				{:else}
					<span class="text-muted">The plugin <span class="font-mono">{plugin}</span> answers it, while it's on under Manager.</span>
				{/if}
			</p>
		{:else}
			<div class="flex flex-wrap items-center gap-2">
				<span class="font-mono text-sm text-muted">/</span>
				<input
					class="input w-36 font-mono"
					placeholder="name"
					maxlength="16"
					spellcheck="false"
					autocomplete="off"
					aria-label="Name players type"
					{disabled}
					value={c.name}
					oninput={(e) => set({ name: e.currentTarget.value.toLowerCase() })}
				/>
				<div class="ml-auto flex items-center gap-2">
					<label class="inline-flex cursor-pointer items-center gap-2" title="Only in-game admins may use it">
						<input type="checkbox" class="peer sr-only" {disabled} checked={c.admin} onchange={(e) => set({ admin: e.currentTarget.checked })} />
						<span class="switch"></span>
						<span class="text-sm text-muted">Admins only</span>
					</label>
					<button type="button" class="btn btn-ghost btn-sm" {disabled} aria-label="Remove /{c.name || 'this command'}" title="Remove" onclick={remove}>
						<Icon name="trash" size={14} />
					</button>
				</div>
			</div>
			<label class="block">
				<span class="label">Reply</span>
				<input class="input" placeholder="Sent back to whoever typed it" {disabled} value={c.reply} oninput={(e) => set({ reply: e.currentTarget.value })} />
			</label>
			<div>
				<span class="label">Server commands, one per line</span>
				<LinesInput
					id="{id}-{i}-command"
					bind:value={() => c.command, (command) => set({ command: command ?? [] })}
					max={maxRuns}
					maxLen={320}
					{disabled}
					placeholder={'announce-to {player} No griefing!'}
				/>
			</div>
		{/if}
	{/snippet}
	{#snippet note()}
		<p class="text-xs text-faint">In a command, {'{player}'} is whoever typed it, {'{map}'} the current map and {'{arg}'} the rest of what they typed.</p>
	{/snippet}
</RowList>
