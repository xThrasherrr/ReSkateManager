import { api } from './api';
import { ask } from './ask.svelte';
import { plural } from './format';
import { instances } from './instances.svelte';
import { attempt } from './toast.svelte';

/** Whether to go ahead with stopping or restarting a server, asked only while players are on. */
export async function dropPlayers(name: string, action: 'stop' | 'restart', players: number): Promise<boolean> {
	if (players <= 0) return true;
	const verb = action === 'stop' ? 'Stop' : 'Restart';
	return ask({
		title: `${verb} ${name}?`,
		body: `${plural(players, 'player is', 'players are')} on. They are dropped${action === 'restart' ? ' and can rejoin once it is back up' : ''}.`,
		action: verb
	});
}

/** Has the manager start, stop, restart or kill a server, then asks for the server list again. */
export async function lifecycle(id: string, action: 'start' | 'stop' | 'restart' | 'kill') {
	await attempt(() => api.post(`/instances/${id}/${action}`));
	instances.refresh();
}
