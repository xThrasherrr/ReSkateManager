// Parks shared as mods (maps.park_mods, ReSkate 2.0.6), as the Parks page
// sets them up: the server checks them as parkMods in
// internal/serverconfig/file.go does, and won't start on one it refuses.
import { shortName } from '#lib/text.js';

export interface ParkMod {
	name: string; // what park-mod add, votes and /parks call it
	folder: string; // the mod's folder in the server's Mods
	map: string; // the map it was built on; it's spawned only there
	key: string; // which of the mod's parks, by base map (bam, grom...); '' for its only one
}

// The base maps' keys, as a mod names its parks/<key>.park.json, and the
// retail map each is (Server/README.txt, RetailMaps in serverconfig/mods.go).
// Custom maps share them, most of them bam's.
export const keyMaps: Record<string, string> = {
	bam: 'San Vansterdam',
	grom: 'Isle of Grom',
	mpr: 'Super Ultra Mega Resort',
	ftue: 'Tutorial Island',
	stadium_1: 'Stadium 1',
	stadium_2: 'Stadium 2'
};

/** Whether the park mod p is the park file key of folder, which has keys parks. */
export const isFile = (p: ParkMod, folder: string, key: string, keys: number) =>
	p.folder === folder && (p.key ? p.key.toLowerCase() === key.toLowerCase() : keys === 1);

/** A name for a park not set up yet: from its mod's folder (the Name of a
 * Thunderstore Namespace-Name-1.2.3), then its key when the mod has more
 * than one, made unique among taken. */
export function suggestName(folder: string, key: string, many: boolean, taken: string[]): string {
	const parts = folder.split('-');
	const from = parts.length === 3 && /^\d+(\.\d+)*$/.test(parts[2]!) ? parts[1]! : folder;
	let base = from.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
	if (many) base = `${base.slice(0, 15 - key.length)}-${key}`;
	base = base.slice(0, 16).replace(/-+$/, '') || 'park';
	let name = base;
	for (let n = 2; taken.includes(name); n++) name = `${base.slice(0, 13)}-${n}`;
	return shortName.test(name) ? name : 'park';
}

// shortcut: a park's box is where its objects' middles are, padded by a few
// units for their size; a real footprint needs each item's bounds, which only
// the game has.
const pad = 4;

/** Whether two parks' boxes (lowest x, y, z, then highest) come within reach. */
export const overlaps = (a: number[], b: number[]) => [0, 1, 2].every((i) => a[i]! - pad < b[i + 3]! + pad && b[i]! - pad < a[i + 3]! + pad);

/** The two axes to draw boxes from above with: the up axis is the one they
 * spread least along, as a park is wider than it is tall. */
export function groundAxes(boxes: number[][]): [number, number] {
	const span = [0, 1, 2].map((i) => Math.max(...boxes.map((b) => b[i + 3]!)) - Math.min(...boxes.map((b) => b[i]!)));
	const up = span.indexOf(Math.min(...span));
	return [0, 1, 2].filter((i) => i !== up) as [number, number];
}
