<script lang="ts">
	import { blankCommand, commandProblems, maxCommands, maxRuns, type CustomCommand } from '#lib/commands.js';
	import Icon from './Icon.svelte';
	import LinesInput from './LinesInput.svelte';

	// The owner's own chat commands (ReSkate 2.0.3): each replies to whoever
	// typed it, runs server commands as the console, or both. The server reads
	// them only when it starts.
	let { id, value = $bindable(), disabled = false }: { id: string; value: CustomCommand[] | undefined; disabled?: boolean } = $props();

	const list = $derived(value ?? []);
	const problems = $derived(commandProblems(list));

	function set(i: number, change: Partial<CustomCommand>) {
		value = list.map((c, j) => (j === i ? { ...c, ...change } : c));
	}

	// From the server's README.
	const examples: CustomCommand[] = [
		{ ...blankCommand(), name: 'discord', reply: 'Join us: discord.gg/…' },
		{ ...blankCommand(), name: 'rules', command: ['announce-to {player} No griefing!'] },
		{ ...blankCommand(), name: 'restart', command: ['map {map}'], admin: true }
	];
	const unused = $derived(examples.filter((e) => !list.some((c) => c.name.trim() === e.name)));
	const add = (c: CustomCommand) => (value = [...list, structuredClone(c)]);
</script>

<div {id} class="space-y-2">
	{#if list.length}
		<ol class="space-y-2">
			{#each list as c, i (i)}
				<li class="space-y-2 rounded-lg border-2 p-3 {problems[i] ? 'border-bad/70' : 'border-line'}">
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
							oninput={(e) => set(i, { name: e.currentTarget.value.toLowerCase() })}
						/>
						<div class="ml-auto flex items-center gap-2">
							<label class="inline-flex cursor-pointer items-center gap-2" title="Only in-game admins may use it">
								<input type="checkbox" class="peer sr-only" {disabled} checked={c.admin} onchange={(e) => set(i, { admin: e.currentTarget.checked })} />
								<span class="switch"></span>
								<span class="text-sm text-muted">Admins only</span>
							</label>
							<button
								type="button"
								class="btn btn-ghost btn-sm"
								{disabled}
								aria-label="Remove /{c.name || 'this command'}"
								title="Remove"
								onclick={() => (value = list.filter((_, j) => j !== i))}
							>
								<Icon name="trash" size={14} />
							</button>
						</div>
					</div>
					<label class="block">
						<span class="label">Reply</span>
						<input
							class="input"
							placeholder="Sent back to whoever typed it"
							{disabled}
							value={c.reply}
							oninput={(e) => set(i, { reply: e.currentTarget.value.replace(/[\r\n]+/g, ' ') })}
						/>
					</label>
					<div>
						<span class="label">Server commands, one per line</span>
						<LinesInput
							id="{id}-{i}-command"
							bind:value={() => c.command, (command) => set(i, { command: command ?? [] })}
							max={maxRuns}
							maxLen={320}
							{disabled}
							placeholder={'announce-to {player} No griefing!'}
						/>
					</div>
					{#if problems[i]}<p class="text-xs text-bad">{problems[i]}</p>{/if}
				</li>
			{/each}
		</ol>
		<p class="text-xs text-faint">In a command, {'{player}'} is whoever typed it, {'{map}'} the current map and {'{arg}'} the rest of what they typed.</p>
	{:else}
		<p class="text-sm text-faint">No chat commands.</p>
	{/if}
	<div class="flex flex-wrap items-center gap-2">
		<button type="button" class="btn btn-sm" disabled={disabled || list.length >= maxCommands} onclick={() => add(blankCommand())}>
			<Icon name="plus" size={14} />Add a command
		</button>
		{#each unused as e (e.name)}
			<button
				type="button"
				class="btn btn-ghost btn-sm"
				disabled={disabled || list.length >= maxCommands}
				title={e.reply || e.command.join('; ')}
				onclick={() => add(e)}
			>
				<Icon name="plus" size={14} />/{e.name}
			</button>
		{/each}
		<span class="ml-auto text-xs text-faint tabular-nums">{list.length}/{maxCommands}</span>
	</div>
</div>
