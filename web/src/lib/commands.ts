// The owner's chat commands (commands, ReSkate 2.0.3), checked as the manager
// checks them (customCommands in internal/serverconfig/file.go), which is as
// the server does: it won't start on a command it refuses.
import { control, utf8Length } from '#lib/text.js';

export interface CustomCommand {
	name: string; // what players type: /<name>
	reply: string; // a chat line back to whoever typed it
	command: string[]; // server commands; {player} is their SteamID64, {map} the current map, {arg} the rest they typed
	admin: boolean; // only admins may use it
}

export const maxCommands = 32;
export const maxRuns = 8;

export const blankCommand = (): CustomCommand => ({ name: '', reply: '', command: [], admin: false });

const commandName = /^[a-z0-9_-]{1,16}$/;
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
	'vote-cancel', 'votes', 'welcome', 'world-layer-sync', 'quit', 'exit', 'stop', 'update'
];

/** What is wrong with each command, by its place in the list ('' for nothing). */
export function commandProblems(list: CustomCommand[]): string[] {
	return list.map((c, i) => {
		const name = c.name.trim();
		if (!commandName.test(name)) return 'The name is 1 to 16 lowercase letters, digits, - or _.';
		if (taken.includes(name) || /^\d+$/.test(name)) return `/${name} is one of the server's own.`;
		if (list.findIndex((o) => o.name.trim() === name) !== i) return `Two commands are called /${name}.`;
		const reply = c.reply.trim();
		if (reply && (utf8Length(reply) > 200 || control.test(reply))) return 'The reply is one chat line of at most 200 bytes.';
		const runs = c.command.map((r) => r.trim()).filter(Boolean);
		if (!reply && !runs.length) return 'Give it a reply, a command, or both.';
		if (runs.length > maxRuns) return `At most ${maxRuns} server commands.`;
		if (runs.some((r) => utf8Length(r) > 320 || control.test(r))) return 'Each server command is one line of at most 320 bytes.';
		return '';
	});
}
