import { untrack } from 'svelte';
import type { Live } from './live.svelte';

/**
 * Calls fn when the server prints a line matching re, such as a change made
 * in the game that a page shows. Lines come in batches (a ban's leave line
 * right behind it), so each new one is looked at. Call it while a component
 * starts.
 */
export function onLine(live: Live, re: RegExp, fn: () => void) {
	let lastSeq = untrack(() => live.console.at(-1)?.seq ?? 0);
	$effect(() => {
		const lines = live.console;
		const end = lines.at(-1)?.seq ?? 0;
		const restarted = end < lastSeq; // the manager restarted and counts from 0 again
		let hit = false;
		for (let i = lines.length - 1; i >= 0 && !hit; i--) {
			const e = lines[i]!;
			if (!restarted && e.seq <= lastSeq) break;
			hit = re.test(e.text);
		}
		lastSeq = end;
		if (hit) untrack(fn);
	});
}

/** A player given a role or losing theirs, as the server says it (ReSkate 2.0.6), or the roles read again. */
export const roleLine = /^\S.* (is now [a-z0-9_-]+|is [a-z0-9_-]+ again|no longer has a role)\.$|^Roles reloaded: /;
