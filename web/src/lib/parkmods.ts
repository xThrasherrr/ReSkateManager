// Parks shared as mods (maps.park_mods, ReSkate 2.0.6), checked as the manager
// checks them (parkMods in internal/serverconfig/file.go), which is as the
// server does: it won't start on a park mod it refuses.
import { repeatsEarlier, shortName, shortNameRule } from '#lib/text.js';

export interface ParkMod {
	name: string; // what park-mod add and votes call it
	folder: string; // the mod's folder in the server's Mods
	map: string; // the map it was built on; it's spawned only there
	key: string; // which of the mod's parks, by base map (bam, grom...); '' for its only one
}

// The base maps' keys, as a mod names its parks/<key>.park.json (Server/README.txt).
export const parkKeys = ['bam', 'grom', 'mpr', 'ftue', 'stadium_1', 'stadium_2'];

export const blankParkMod = (): ParkMod => ({ name: '', folder: '', map: '', key: '' });

const folderName = /^[A-Za-z0-9_\-. ]{1,64}$/;
const keyName = /^[a-z0-9_]{1,32}$/;

/** What is wrong with each park mod, by its place in the list ('' for nothing). */
export function parkModProblems(list: ParkMod[]): string[] {
	return list.map((p, i) => {
		const name = p.name.trim();
		if (!shortName.test(name)) return shortNameRule;
		if (repeatsEarlier(list, i, name)) return `Two park mods are called ${name}.`;
		if (!folderName.test(p.folder) || /^[. ]|\s$/.test(p.folder)) return "Pick the mod's folder in Mods.";
		if (!p.map.trim()) return 'Pick the map it was built on.';
		if (p.key.trim() && !keyName.test(p.key.trim())) return "The key is a base map's, such as bam or grom, or empty.";
		return '';
	});
}
