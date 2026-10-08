<script lang="ts">
	import { onMount } from 'svelte';
	import { api, message, type Grant, type Role, type User } from '#lib/api.js';
	import { session } from '#lib/session.svelte.js';
	import { instances } from '#lib/instances.svelte.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { ago, steamProfile } from '#lib/format.js';
	import Modal from '#lib/components/Modal.svelte';
	import Icon from '#lib/components/Icon.svelte';
	import { Busy } from '#lib/busy.svelte.js';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import { ask } from '#lib/ask.svelte.js';

	let users = $state.raw<User[]>([]);
	let roles = $state.raw<Role[]>([]);

	let loaded = $state(false);
	let loadError = $state('');
	async function load() {
		try {
			const [u, r] = await Promise.all([api.get<User[]>('/users'), api.get<Role[]>('/roles')]);
			users = u;
			roles = r;
			loaded = true;
			loadError = '';
		} catch (e) {
			loadError = message(e);
		}
	}
	onMount(load);

	const perms = $derived(session.meta?.permissions ?? []);
	const instanceName = (id: string) => (id === '*' ? 'All servers' : (instances.byId(id)?.name ?? id));

	// ---- user editor ----
	let editing = $state.raw<User | null>(null);
	let open = $state(false);
	let form = $state({ username: '', password: '', steamId: '', owner: false, disabled: false, grants: [] as Grant[] });

	function edit(u: User | null) {
		editing = u;
		form = {
			username: u?.username ?? '',
			password: '',
			steamId: u?.steamId ?? '',
			owner: u?.owner ?? false,
			disabled: u?.disabled ?? false,
			grants: u ? u.grants.map((g) => ({ roleId: g.roleId, instance: g.instance })) : []
		};
		open = true;
	}

	const busy = new Busy();
	function saveUser(e: SubmitEvent) {
		e.preventDefault();
		return busy.run('user', sendUser);
	}
	async function sendUser() {
		const grants = form.grants.filter((g) => g.roleId);
		let ok;
		if (editing) {
			const body: Record<string, unknown> = { grants };
			if (form.username !== editing.username) body.username = form.username;
			if (form.password) body.password = form.password;
			if (form.steamId.trim() !== (editing.steamId ?? '')) body.steamId = form.steamId.trim();
			if (form.owner !== editing.owner) body.owner = form.owner;
			if (form.disabled !== editing.disabled) body.disabled = form.disabled;
			ok = await attempt(() => api.patch(`/users/${editing!.id}`, body), 'User saved');
		} else {
			ok = await attempt(
				() => api.post('/users', { username: form.username, password: form.password, steamId: form.steamId.trim(), owner: form.owner, grants }),
				'User created'
			);
		}
		if (ok) {
			open = false;
			load();
		}
	}

	// The dialog closes itself on Escape, so its open flag is bound rather than
	// derived from the target; a one-way flag would stay stuck closed.
	let delTarget = $state.raw<User | null>(null);
	let delOpen = $state(false);
	function askRemove(u: User) {
		delTarget = u;
		delOpen = true;
	}
	async function removeUser() {
		const u = delTarget!;
		delOpen = false;
		if (await attempt(() => api.del(`/users/${u.id}`), `Removed ${u.username}`)) load();
	}

	// ---- role editor ----
	let roleOpen = $state(false);
	let role = $state<Role>({ id: 0, name: '', permissions: [], builtin: false });
	function editRole(r: Role | null) {
		role = r ? structuredClone(r) : { id: 0, name: '', permissions: [], builtin: false };
		roleOpen = true;
	}
	async function saveRole(e: SubmitEvent) {
		e.preventDefault();
		const body = { name: role.name.trim(), permissions: role.permissions };
		const id = role.id;
		const ok = await busy.run('role', () =>
			id ? attempt(() => api.patch(`/roles/${id}`, body), 'Role saved') : attempt(() => api.post('/roles', body), 'Role created')
		);
		if (ok) {
			roleOpen = false;
			load();
		}
	}
	async function deleteRole() {
		const id = role.id;
		const go = await ask({ title: `Delete the role ${role.name}?`, body: 'Everyone granted it loses what it allowed.', action: 'Delete role' });
		if (!go) return;
		if (await busy.run('delete-role', () => attempt(() => api.del(`/roles/${id}`), 'Role removed'))) {
			roleOpen = false;
			load();
		}
	}
	function togglePerm(key: string, on: boolean) {
		role.permissions = on ? [...role.permissions, key] : role.permissions.filter((p) => p !== key);
	}

	// For pattern attributes, where Svelte would read the braces as its own.
	const usernamePattern = String.raw`[A-Za-z0-9_.\-]{3,32}`;
	const steamIdPattern = String.raw`\s*7656119[0-9]{10}\s*`;
	const notBlank = String.raw`.*\S.*`;
</script>

<div class="mx-auto max-w-6xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	<div>
		<h1 class="page-title">Users &amp; roles</h1>
		<p class="mt-2 text-sm text-muted">Who can sign in to this panel and what they can do. Owners can do everything.</p>
		{#if !session.perms?.owner}
			<p class="mt-1 text-xs text-faint">You can only grant permissions you hold, and only edit users and roles that hold no more than you do.</p>
		{/if}
	</div>

	{#if loadError}<LoadError error={loadError} onretry={load} />{/if}

	<section class="card">
		<div class="flex items-center justify-between border-b-2 border-line px-4 py-3">
			<h2>Users</h2>
			<button class="btn btn-primary btn-sm" onclick={() => edit(null)}><Icon name="plus" size={14} /> Add user</button>
		</div>
		{#if !loaded && !loadError}<Loading class="px-4 py-6" />{/if}
		<div class="overflow-x-auto">
			<table class="table">
				<thead><tr><th>User</th><th class="hidden md:table-cell">Sign-in</th><th>Access</th><th class="hidden sm:table-cell">Last sign-in</th><th></th></tr></thead>
				<tbody>
					{#each users as u (u.id)}
						<tr class={u.disabled ? 'opacity-50' : ''}>
							<td>
								<span class="font-medium">{u.username}</span>
								{#if u.owner}<span class="sticker sticker-warn ml-2">Owner</span>{/if}
								{#if u.disabled}<span class="ml-2 text-xs text-faint">disabled</span>{/if}
							</td>
							<td class="hidden text-xs text-muted md:table-cell">
								{#if u.hasPassword}Password{/if}{#if u.hasPassword && u.steamId} · {/if}{#if u.steamId}<a
										class="hover:text-fg"
										href={steamProfile(u.steamId)}
										target="_blank"
										rel="noreferrer">Steam</a
									>{/if}
							</td>
							<td class="text-xs">
								{#if u.owner}<span class="text-muted">Everything</span>
								{:else if u.grants.length}
									{#each u.grants as g (`${g.roleId}:${g.instance}`)}<span class="mr-1 mb-1 inline-block rounded-md border border-line-strong bg-raised px-1.5 py-0.5"
											>{g.role} <span class="text-faint">· {instanceName(g.instance)}</span></span
										>{/each}
								{:else}<span class="text-faint">No access</span>{/if}
							</td>
							<td class="hidden whitespace-nowrap text-muted sm:table-cell">{u.lastLoginAt ? ago(u.lastLoginAt * 1000) : 'Never'}</td>
							<td class="text-right whitespace-nowrap">
								{#if !u.owner || session.perms?.owner}
									<button class="btn btn-sm btn-ghost" onclick={() => edit(u)}>Edit</button>
									{#if u.id !== session.user?.id}<button class="btn btn-sm btn-ghost text-bad" onclick={() => askRemove(u)} aria-label="Remove {u.username}"
											><Icon name="trash" size={14} /></button
										>{/if}
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</section>

	<section class="card">
		<div class="flex items-center justify-between border-b-2 border-line px-4 py-3">
			<h2>Roles</h2>
			<button class="btn btn-sm" onclick={() => editRole(null)}><Icon name="plus" size={14} /> New role</button>
		</div>
		{#if !loaded && !loadError}<Loading class="px-4 py-6" />{/if}
		<table class="table">
			<thead><tr><th>Role</th><th class="hidden sm:table-cell">Permissions</th><th></th></tr></thead>
			<tbody>
				{#each roles as r (r.id)}
					{@const list = r.permissions.map((p) => perms.find((x) => x.key === p)?.label ?? p).join(' · ') || 'None'}
					<tr>
						<td class="font-medium sm:whitespace-nowrap">
							{r.name}{#if r.builtin}<span class="ml-2 text-xs font-normal text-faint">built-in</span>{/if}
							<div class="mt-1 text-xs font-normal text-muted sm:hidden">{list}</div>
						</td>
						<td class="hidden text-xs text-muted sm:table-cell">{list}</td>
						<td class="text-right"><button class="btn btn-sm btn-ghost" onclick={() => editRole(r)}>Edit</button></td>
					</tr>
				{/each}
			</tbody>
		</table>
	</section>
</div>

<Modal bind:open title={editing ? `Edit ${editing.username}` : 'Add user'} wide>
	<form id="userform" onsubmit={saveUser} class="space-y-4">
		<div class="grid gap-3 sm:grid-cols-2">
			<div>
				<label class="label" for="un">Username</label>
				<input id="un" class="input" bind:value={form.username} required minlength="3" maxlength="32" pattern={usernamePattern} title="3-32 letters, digits and _ . -" />
			</div>
			<div>
				<label class="label" for="pw">{editing ? 'New password' : 'Password'}</label>
				<input id="pw" class="input" type="password" autocomplete="new-password" bind:value={form.password} minlength="10" placeholder={editing ? 'Leave blank to keep' : 'Optional with Steam'} />
			</div>
			<div class="sm:col-span-2">
				<label class="label" for="sid">Steam account (SteamID64)</label>
				<input id="sid" class="input font-mono" bind:value={form.steamId} placeholder="7656119…" pattern={steamIdPattern} title="A SteamID64: 17 digits starting 7656119" />
				<p class="mt-1 text-xs text-faint">Lets them sign in with Steam. Users can also link their own account from their profile.</p>
			</div>
		</div>
		{#if session.perms?.owner}
			<label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={form.owner} /> Owner — full access, including other owners</label>
		{/if}
		{#if editing}
			<label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={form.disabled} /> Disabled — cannot sign in</label>
		{/if}
		{#if !form.owner}
			<div>
				<div class="mb-1 flex items-center justify-between">
					<span class="label mb-0">Roles</span>
					<button type="button" class="btn btn-sm btn-ghost" onclick={() => form.grants.push({ roleId: roles[0]?.id ?? 0, instance: '*' })}
						><Icon name="plus" size={14} /> Add role</button
					>
				</div>
				<div class="space-y-2">
					{#each form.grants as g, i (i)}
						<div class="flex gap-2">
							<select class="input min-w-0 flex-1" aria-label="Role" bind:value={g.roleId}>
								{#each roles as r (r.id)}<option value={r.id}>{r.name}</option>{/each}
							</select>
							<span class="hidden self-center text-sm text-faint sm:inline">on</span>
							<select class="input min-w-0 flex-1" aria-label="On" bind:value={g.instance}>
								<option value="*">All servers</option>
								{#each instances.list as inst (inst.id)}<option value={inst.id}>{inst.name}</option>{/each}
							</select>
							<button type="button" class="btn btn-ghost px-2" onclick={() => form.grants.splice(i, 1)} aria-label="Remove role"><Icon name="x" size={14} /></button>
						</div>
					{:else}
						<p class="text-sm text-faint">No roles: the user can sign in but sees nothing.</p>
					{/each}
				</div>
				<p class="mt-2 text-xs text-faint">Panel-wide permissions (users, servers, audit log) only count on “All servers”.</p>
			</div>
		{/if}
	</form>
	{#snippet actions()}
		<button class="btn" onclick={() => (open = false)}>Cancel</button>
		<button class="btn btn-primary" form="userform" disabled={busy.is()}>{busy.is('user') ? 'Saving…' : 'Save'}</button>
	{/snippet}
</Modal>

<Modal bind:open={roleOpen} title={role.id ? `Edit ${role.name}` : 'New role'} wide>
	<form id="roleform" onsubmit={saveRole} class="space-y-4">
		<div>
			<label class="label" for="rn">Name</label>
			<input id="rn" class="input" bind:value={role.name} required maxlength="48" pattern={notBlank} title="A name, not only spaces" />
		</div>
		<div class="grid gap-x-6 gap-y-2 sm:grid-cols-2">
			{#each perms as p (p.key)}
				<label class="flex items-start gap-2 text-sm">
					<input
						type="checkbox"
						class="mt-0.5"
						checked={role.permissions.includes(p.key)}
						onchange={(e) => togglePerm(p.key, e.currentTarget.checked)}
					/>
					<span>{p.label}{#if p.global}<span class="ml-1 text-xs text-faint">(panel-wide)</span>{/if}</span>
				</label>
			{/each}
		</div>
	</form>
	{#snippet actions()}
		{#if role.id}<button class="btn btn-danger mr-auto" disabled={busy.is()} onclick={deleteRole}>Delete role</button>{/if}
		<button class="btn" onclick={() => (roleOpen = false)}>Cancel</button>
		<button class="btn btn-primary" form="roleform" disabled={busy.is()}>{busy.is('role') ? 'Saving…' : 'Save'}</button>
	{/snippet}
</Modal>

<Modal bind:open={delOpen} title="Remove {delTarget?.username}?">
	<p class="text-sm text-muted">They are signed out everywhere and can no longer reach the panel.</p>
	{#snippet actions()}
		<button class="btn" onclick={() => (delOpen = false)}>Cancel</button>
		<button class="btn btn-danger" onclick={removeUser}>Remove</button>
	{/snippet}
</Modal>
