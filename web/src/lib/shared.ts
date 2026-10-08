import type { SharedUse } from './api';

/** The choices for how a server uses the shared mods, in display order. */
export const sharedUses: { use: SharedUse; label: string; help: string }[] = [
	{ use: 'off', label: 'Off', help: 'Only the mods in its own Mods folder load.' },
	{ use: 'all', label: 'All', help: 'Every shared mod loads, and so does each one shared later. Any one can still be turned off.' },
	{
		use: 'pick',
		label: 'Only picked',
		help: 'Only the shared mods turned on for it load. Ones shared later arrive turned off, which suits a server that runs one map.'
	}
];

export const sharedHelp = (use: SharedUse) => sharedUses.find((u) => u.use === use)?.help ?? '';

/** Whether changing from one use to the other adds or removes links, and so wants a confirm. */
export const relinks = (from: SharedUse, to: SharedUse) => (from === 'off') !== (to === 'off');

/** The toast after a server's use changed; swapped counts its copies that became links. */
export function sharedDone(name: string, use: SharedUse, swapped: number) {
	const copies = swapped ? `; ${swapped} matching cop${swapped === 1 ? 'y' : 'ies'} became links` : '';
	if (use === 'off') return `${name} no longer uses the shared mods`;
	return use === 'all' ? `${name} loads every shared mod${copies}` : `${name} loads only the shared mods picked for it${copies}`;
}
