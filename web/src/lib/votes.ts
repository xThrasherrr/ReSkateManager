// The owner's custom votes (votes.custom, ReSkate 2.0.2), checked as the
// manager checks them (customVotes in internal/serverconfig/file.go), which is
// as the server does: it won't start on a vote it refuses.
import { control, utf8Length } from '#lib/text.js';

export interface CustomVote {
	name: string; // what players type: /vote <name>
	description: string; // for /vote list and the vote card
	command: string; // a server command; {map} is the current map, {arg} the choice
	choices: string[];
	enabled: boolean;
	percent: number;
	seconds: number; // 0: the votes' length
	cooldown_seconds: number; // 0: the votes' cooldown
	min_players: number;
}

export const maxVotes = 16;
export const maxChoices = 8;

export const blankVote = (): CustomVote => ({
	name: '',
	description: '',
	command: '',
	choices: [],
	enabled: true,
	percent: 60,
	seconds: 0,
	cooldown_seconds: 0,
	min_players: 1
});

const voteName = /^[a-z0-9_-]{1,16}$/;
// What /vote <word> already means, and numbers, which answer a poll.
const taken = ['map', 'kick', 'tod', 'time', 'yes', 'y', 'no', 'n', 'poll', 'list'];

const whole = (n: unknown, min: number, max: number) => typeof n === 'number' && Number.isInteger(n) && n >= min && n <= max;

/** What is wrong with each vote, by its place in the list ('' for nothing). */
export function voteProblems(list: CustomVote[]): string[] {
	return list.map((v, i) => {
		const name = v.name.trim();
		if (!voteName.test(name)) return 'The name is 1 to 16 lowercase letters, digits, - or _.';
		if (taken.includes(name) || /^\d+$/.test(name)) return `/vote ${name} is one of the server's own.`;
		if (list.findIndex((o) => o.name.trim() === name) !== i) return `Two votes are called ${name}.`;
		const description = v.description.trim();
		if (description && (utf8Length(description) > 80 || control.test(description))) return 'The description is one line of at most 80 bytes.';
		const command = v.command.trim();
		if (!command) return 'Give it a command, such as map {map}.';
		if (utf8Length(command) > 320 || control.test(command)) return 'The command is one line of at most 320 bytes.';
		if (v.choices.length > maxChoices) return `At most ${maxChoices} choices.`;
		const bad = v.choices.find((c) => !voteName.test(c));
		if (bad !== undefined) return `Choices are 1 to 16 lowercase letters, digits, - or _ (${bad} is not).`;
		const arg = command.includes('{arg}');
		if (arg && !v.choices.length) return 'A command with {arg} needs choices to fill it.';
		if (!arg && v.choices.length) return 'Choices need {arg} in the command, where the choice goes.';
		if (!whole(v.percent, 1, 100)) return 'The pass percentage is 1 to 100.';
		if (!whole(v.seconds, 0, 300) || (v.seconds > 0 && v.seconds < 10)) return 'The length is 0 (the votes’ length) or 10 to 300 s.';
		if (!whole(v.cooldown_seconds, 0, 3600)) return 'The cooldown is 0 to 3600 s.';
		if (!whole(v.min_players, 1, 249)) return 'Players on is 1 to 249.';
		return '';
	});
}
