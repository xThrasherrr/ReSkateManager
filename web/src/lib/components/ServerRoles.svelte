<script lang="ts">
	import { api, type Admin, type RoleList, type ServerRole } from '#lib/api.js';
	import { ask } from '#lib/ask.svelte.js';
	import { Busy } from '#lib/busy.svelte.js';
	import { useLive } from '#lib/context.js';
	import { steamProfile } from '#lib/format.js';
	import { instances } from '#lib/instances.svelte.js';
	import { allItCanUse, canUse, giveable, maxRoleRank, maxRoleTag, maxRoles, validRoleName } from '#lib/roles.js';
	import { hexColour, isSteamId, utf8Length } from '#lib/text.js';
	import { attempt, toast } from '#lib/toast.svelte.js';
	import ColourInput from './ColourInput.svelte';
	import Icon from './Icon.svelte';
	import Modal from './Modal.svelte';
	import RoleTag from './RoleTag.svelte';

	// A server's roles (ReSkate 2.0.6, data/roles.json) and who has each: give
	// one by SteamID64, and make, change and delete them. seen offers the
	// players the server has had by name.
	let { roles, seen = [], onchange }: { roles: RoleList; seen?: { id: string; name: string }[]; onchange: () => void } = $props();

	const ctx = useLive();
	const busy = new Busy();
	const given = $derived(roles.roles.reduce((n, r) => n + r.players.length, 0));
	// Other servers this user could give the role on too.
	const others = $derived(instances.list.filter((i) => i.id !== ctx.id && i.permissions?.includes('ingame.admins.manage')).length);

	// Give a role: to whom, which, and on every server the user manages admins on.
	let giveId = $state('');
	let giveName = $state('');
	let everywhere = $state(false);
	async function giveRole(e: SubmitEvent) {
		e.preventDefault();
		const body = { id: giveId.trim(), role: giveName, everywhere: everywhere && others > 0 };
		const r = await busy.run('give', () =>
			attempt(() => api.post<{ reply?: string; results?: { name: string; reply?: string; error?: string }[] }>(`/instances/${ctx.id}/roles/players`, body))
		);
		if (!r) return;
		if (r.results) {
			const done = r.results.filter((x) => !x.error);
			if (done.length) toast.ok(`${body.role} given on ${done.map((x) => x.name).join(', ')}.`);
			for (const x of r.results.filter((x) => x.error)) toast.error(`${x.name}: ${x.error}`);
		} else if (r.reply) toast.ok(r.reply);
		giveId = '';
		onchange();
	}
	async function takeRole(role: ServerRole, pl: Admin) {
		const who = pl.name || pl.id;
		if (!(await ask({ title: `Take ${who}'s ${role.name} role?`, body: `They're ${roles.default || 'given no role'} again on this server.`, action: 'Take it' }))) return;
		if (await busy.run(`take:${pl.id}`, () => attempt(() => api.del<{ reply: string }>(`/instances/${ctx.id}/roles/players/${pl.id}`), (r) => r.reply))) onchange();
	}

	// The role being made ('') or changed (its name), in a dialog.
	let editing = $state('');
	let open = $state(false);
	let form = $state({ name: '', tag: '', color: '#FFFFFF', rank: 0 });
	function edit(r?: ServerRole) {
		editing = r?.name ?? '';
		form = { name: r?.name ?? '', tag: r?.tag ?? '', color: r?.color || '#FFFFFF', rank: r?.rank ?? 0 };
		open = true;
	}
	const problem = $derived.by(() => {
		const name = form.name.trim().toLowerCase();
		if (!editing && !validRoleName(name)) return 'The name is 1 to 24 lowercase letters, digits, - or _, and not none.';
		if (!editing && roles.roles.some((r) => r.name === name)) return 'The server has a role by that name already.';
		if (utf8Length(form.tag.trim()) > maxRoleTag) return `A title is at most ${maxRoleTag} letters.`;
		if (!hexColour.test(form.color)) return 'The colour is like #FF74C6.';
		if (!Number.isInteger(form.rank) || form.rank < 0 || form.rank > maxRoleRank) return `The rank is 0 to ${maxRoleRank}.`;
		return '';
	});
	async function save() {
		const look = { tag: form.tag.trim(), color: form.color, rank: form.rank };
		const path = `/instances/${ctx.id}/roles`;
		const done = await busy.run('save', () =>
			editing
				? attempt(() => api.patch<{ reply: string }>(`${path}/${encodeURIComponent(editing)}`, look), (r) => r.reply)
				: attempt(() => api.post<{ reply: string }>(path, { name: form.name.trim().toLowerCase(), ...look }), (r) => r.reply)
		);
		if (done) {
			open = false;
			onchange();
		}
	}

	// A role given to many shows a few of them until asked for the rest.
	const chips = 8;
	const shownAll = $state<Record<string, boolean>>({});

	// Why a role can't be deleted, or '' when it can.
	const stays = (r: ServerRole) =>
		r.name === roles.default ? "The default role stays: it's everyone's who was given none" : r.name === 'admin' ? 'The admin role stays: the manager gives it to its in-game admins' : '';
	async function remove(r: ServerRole) {
		const n = r.players.length;
		const who = n === 1 ? 'The player who has it is' : `The ${n} players who have it are`;
		const body = !n ? 'Nobody has it.' : roles.default ? `${who} ${roles.default} again.` : `${who} given no role.`;
		if (!(await ask({ title: `Delete the ${r.name} role?`, body, action: 'Delete' }))) return;
		if (await busy.run(`del:${r.name}`, () => attempt(() => api.del<{ reply: string }>(`/instances/${ctx.id}/roles/${encodeURIComponent(r.name)}`), (x) => x.reply))) onchange();
	}
</script>

<section class="card min-w-0">
	<div class="flex items-start justify-between gap-3 border-b-2 border-line px-4 py-3">
		<div class="min-w-0 flex-1">
			<h2 class="flex items-center gap-2">Roles <span class="sticker sticker-info">{given}</span></h2>
			<p class="mt-0.5 text-xs text-faint">
				What players wear and may do in the game, from the server's data/roles.json. Give one here or from the lists below, and take it away with the × by
				a player's name. A new role has what the default role has; README-roles.txt beside the server lists the commands and permissions a role can get.
			</p>
		</div>
		<button class="btn btn-sm shrink-0" disabled={roles.roles.length >= maxRoles || busy.is()} onclick={() => edit()}><Icon name="plus" size={14} /> New role</button>
	</div>
	<form onsubmit={giveRole} class="flex flex-col gap-2 border-b-2 border-line bg-sunken/40 px-4 py-3 sm:flex-row sm:items-center">
		<input class="input font-mono" list="role-players" placeholder="SteamID64" aria-label="SteamID64 to give a role" bind:value={giveId} required />
		<datalist id="role-players">{#each seen as pl (pl.id)}<option value={pl.id}>{pl.name}</option>{/each}</datalist>
		<select class="input sm:w-44" aria-label="Role to give" bind:value={giveName} required>
			<option value="" disabled>Role…</option>
			{#each giveable(roles) as r (r.name)}<option value={r.name}>{r.name}</option>{/each}
		</select>
		{#if others}
			<label class="inline-flex shrink-0 cursor-pointer items-center gap-2" title="Each server has its own roles: it's given where a role of that name exists">
				<input type="checkbox" class="peer sr-only" bind:checked={everywhere} />
				<span class="switch"></span>
				<span class="text-sm text-muted">Every server</span>
			</label>
		{/if}
		<button class="btn shrink-0" disabled={!isSteamId(giveId) || !giveName || busy.is()}><Icon name="plus" size={14} /> {busy.is('give') ? 'Giving…' : 'Give role'}</button>
	</form>
	<div class="overflow-x-auto">
		<table class="table">
			<thead><tr><th>Role</th><th class="hidden md:table-cell">Rank</th><th class="hidden sm:table-cell">Can use</th><th>Players</th><th></th></tr></thead>
			<tbody>
				{#each roles.roles as r (r.name)}
					<tr>
						<td class="whitespace-nowrap">
							<span class="font-medium">{r.name}</span>
							{#if r.tag}<RoleTag tag={r.tag} color={r.color} class="ml-2" />{/if}
							{#if r.name === roles.default}<span class="ml-2 rounded bg-raised px-1.5 py-0.5 text-[10px] text-muted" title="Everyone given no role has it">default</span>{/if}
						</td>
						<td class="hidden text-muted tabular-nums md:table-cell">{r.rank}</td>
						<td class="hidden text-sm text-muted sm:table-cell" title={allItCanUse(r)}>
							{canUse(r)}{#if !r.inherit}<span class="text-faint">{' · only its own'}</span>{/if}
						</td>
						<td>
							{#if r.name === roles.default}
								<span class="text-sm text-faint">everyone else</span>
							{:else if r.players.length}
								<div class="flex flex-wrap gap-1.5">
									{#each shownAll[r.name] ? r.players : r.players.slice(0, chips) as pl (pl.id)}
										<span class="inline-flex items-center rounded bg-raised text-xs">
											<a class="px-1.5 py-0.5 hover:text-fg {pl.name ? '' : 'font-mono text-[11px] text-muted'}" href={steamProfile(pl.id)} target="_blank" rel="noreferrer" title={pl.id}>{pl.name || pl.id}</a>
											{#if pl.panel}
												<span class="pr-1.5 text-[10px] text-faint" title="An admin kept in step with this panel user's roles">panel · {pl.panel}</span>
											{:else}
												<button
													class="px-1 text-faint hover:text-bad disabled:opacity-50"
													disabled={busy.is()}
													title="Take their role away"
													aria-label="Take {pl.name || pl.id}'s {r.name} role"
													onclick={() => takeRole(r, pl)}><Icon name="x" size={12} /></button
												>
											{/if}
										</span>
									{/each}
									{#if r.players.length > chips}
										<button class="rounded px-1.5 py-0.5 text-xs text-accent hover:underline" onclick={() => (shownAll[r.name] = !shownAll[r.name])}>
											{shownAll[r.name] ? 'show fewer' : `+${r.players.length - chips} more`}
										</button>
									{/if}
								</div>
							{:else}
								<span class="text-sm text-faint">nobody</span>
							{/if}
						</td>
						<td>
							<div class="flex justify-end gap-1">
								<button class="btn btn-sm btn-ghost" disabled={busy.is()} onclick={() => edit(r)} title="Change its title, colour and rank" aria-label="Change the {r.name} role"><Icon name="pencil" size={14} /></button>
								<button class="btn btn-sm btn-ghost hover:text-bad" disabled={!!stays(r) || busy.is()} onclick={() => remove(r)} title={stays(r) || 'Delete this role'} aria-label="Delete the {r.name} role"><Icon name="trash" size={14} /></button>
							</div>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</section>

<Modal bind:open title={editing ? `The ${editing} role` : 'New role'}>
	<div class="space-y-3">
		{#if !editing}
			<label class="block">
				<span class="label">Name</span>
				<input class="input font-mono" bind:value={form.name} maxlength="24" placeholder="helper" autocomplete="off" spellcheck="false" />
				<span class="mt-1 block text-xs text-faint">Lowercase letters, digits, - and _. Players are given the role by its name, which stays.</span>
			</label>
		{/if}
		<label class="block">
			<span class="label">Title</span>
			<input class="input" bind:value={form.tag} maxlength={maxRoleTag} placeholder="Helper" autocomplete="off" />
			<span class="mt-1 block text-xs text-faint">Shown before their names in chat and on their nametags. Up to {maxRoleTag} letters, or none.</span>
		</label>
		<div>
			<label class="label" for="role-color">Title colour</label>
			<ColourInput id="role-color" bind:value={form.color} />
		</div>
		<label class="block">
			<span class="label">Rank</span>
			<input class="input w-28" type="number" min="0" max={maxRoleRank} step="1" bind:value={form.rank} />
			<span class="mt-1 block text-xs text-faint">
				0 to {maxRoleRank}. Nobody can kick, ban or change the role of a player whose rank is the same as theirs or higher. A new server's admin role is 100,
				moderator 50 and vip 10.
			</span>
		</label>
		{#if form.tag.trim()}
			<p class="flex items-center gap-2 text-sm text-muted">In the game: <RoleTag tag={form.tag.trim()} color={form.color} /></p>
		{/if}
		{#if problem}<p class="text-xs text-bad">{problem}</p>{/if}
	</div>
	{#snippet actions()}
		<button class="btn" onclick={() => (open = false)}>Cancel</button>
		<button class="btn btn-primary" disabled={!!problem || busy.is()} onclick={save}>{busy.is('save') ? 'Saving…' : editing ? 'Save' : 'Add role'}</button>
	{/snippet}
</Modal>
