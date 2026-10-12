// The owner's chat commands (commands, ReSkate 2.0.3), checked as the manager
// checks them (customCommands in internal/serverconfig/file.go), which is as
// the server does: it won't start on a command it refuses.
import { digits, oneLine, repeatsEarlier, shortName, shortNameRule } from '#lib/text.js';

export interface CustomCommand {
	name: string; // what players type: /<name>
	reply: string; // a chat line back to whoever typed it
	command: string[]; // server commands; {player} is their SteamID64, {map} the current map, {arg} the rest they typed
	admin: boolean; // only admins may use it
}

export const maxRuns = 8;

export const blankCommand = (): CustomCommand => ({ name: '', reply: '', command: [], admin: false });

// The chat's own commands, and the server commands admins type as /<command>.
const taken = [
	'help', 'party', 'p', 'w', 'whisper', 'tell', 'poll', 'vote', 'yes', 'y', 'no', 'n', 'tp',
	'activity-log', 'admin', 'admins', 'afk-kick', 'announce', 'announce-throwdowns', 'announce-to', 'announcements',
	'ban', 'bans', 'bone-scale', 'boosts', 'boosts-allow', 'chat-color', 'chat-colour', 'clear-objects', 'crowd',
	'distances', 'effects', 'kick', 'layer', 'layer-sync', 'layers', 'listed', 'map', 'map-pool', 'maps', 'msg',
	'msg-admins', 'msg-party', 'name', 'net', 'nobail', 'nobail-allow', 'noclip', 'noclip-allow', 'object-limit',
	'object-placement', 'object-scaling', 'objects', 'park', 'parties', 'party-size', 'password', 'placement',
	'players', 'rate', 'reserved', 'rotation', 'say', 'score-allow', 'score-check', 'speed-check', 'status', 'tod',
	'tpall', 'tphere', 'tps', 'tuning', 'tuning-enforce', 'unban', 'voice', 'voice-allow', 'voice-range',
	'vote-cancel', 'votes', 'welcome', 'world-layer-sync', 'quit', 'exit', 'stop', 'update',
	'role', 'roles', 'board-check', 'park-mod', 'teleport' // 2.0.6
];

/** What is wrong with each command, by its place in the list ('' for nothing). */
export function commandProblems(list: CustomCommand[]): string[] {
	return list.map((c, i) => {
		const name = c.name.trim();
		if (!shortName.test(name)) return shortNameRule;
		if (taken.includes(name) || digits.test(name)) return `/${name} is one of the server's own.`;
		if (repeatsEarlier(list, i, name)) return `Two commands are called /${name}.`;
		const reply = c.reply.trim();
		if (reply && !oneLine(reply, 200)) return 'The reply is one chat line of at most 200 bytes.';
		const runs = c.command.map((r) => r.trim()).filter(Boolean);
		if (!reply && !runs.length) return 'Give it a reply, a command, or both.';
		if (runs.length > maxRuns) return `At most ${maxRuns} server commands.`;
		if (runs.some((r) => !oneLine(r, 320))) return 'Each server command is one line of at most 320 bytes.';
		return '';
	});
}
