<script lang="ts">
	import { api, type RoleList } from '#lib/api.js';
	import { ask } from '#lib/ask.svelte.js';
	import { useLive } from '#lib/context.js';
	import { canUse, giveable, panelAdmin, roleOf } from '#lib/roles.js';
	import { attempt } from '#lib/toast.svelte.js';

	// A player's role on this server, changed by picking another: given at once,
	// with the role command while the server runs, else in its data/roles.json.
	let { id, name = '', roles, onchange }: { id: string; name?: string; roles: RoleList; onchange: () => void } = $props();

	const ctx = useLive();
	const current = $derived(roleOf(roles, id)?.name ?? '');
	const kept = $derived(panelAdmin(roles, id));
	const who = $derived(name || id);
	let busy = $state(false);

	async function pick(e: Event & { currentTarget: HTMLSelectElement }) {
		const select = e.currentTarget;
		const role = select.value;
		const r = roles.roles.find((x) => x.name === role);
		// A role with commands lets them act on other players: asked first.
		if (r?.commands.length && !(await ask({ title: `Make ${who} ${role}?`, body: `The ${role} role can use ${canUse(r)}.`, action: 'Give role', danger: false }))) {
			select.value = current;
			return;
		}
		busy = true;
		const done = role
			? await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/roles/players`, { id, role }), (x) => x.reply)
			: await attempt(() => api.del<{ reply: string }>(`/instances/${ctx.id}/roles/players/${id}`), (x) => x.reply);
		busy = false;
		if (done) onchange();
		else select.value = current;
	}
</script>

<select
	class="input h-7 w-auto max-w-40 py-0 pr-7 text-xs"
	aria-label="{who}'s role"
	disabled={busy || kept}
	title={kept ? "An admin kept in step with a panel user's roles" : undefined}
	onchange={pick}
>
	<option value="" selected={current === ''}>{roles.default || 'no role'}</option>
	{#each giveable(roles) as r (r.name)}<option value={r.name} selected={current === r.name}>{r.name}</option>{/each}
</select>
