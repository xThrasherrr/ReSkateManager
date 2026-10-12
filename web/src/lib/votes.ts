// The owner's custom votes (votes.custom, ReSkate 2.0.2), checked as the
// manager checks them (customVotes in internal/serverconfig/file.go), which is
// as the server does: it won't start on a vote it refuses.
import { digits, oneLine, repeatsEarlier, shortName, shortNameRule } from '#lib/text.js';

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

const maxChoices = 8;

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

// What /vote <word> already means, and numbers, which answer a poll.
const taken = ['map', 'kick', 'tod', 'time', 'yes', 'y', 'no', 'n', 'poll', 'list'];

/** A vote's numbers, with the range each takes and what is said when one is out of it. */
export const voteNumbers = [
	{ key: 'percent', label: '% to pass', min: 1, max: 100, rule: 'The pass percentage is 1 to 100.' },
	{ key: 'seconds', label: 'Length (s)', min: 0, max: 300, rule: 'The length is 0 (the votes’ length) or 10 to 300 s.' },
	{ key: 'cooldown_seconds', label: 'Cooldown (s)', min: 0, max: 3600, rule: 'The cooldown is 0 to 3600 s.' },
	{ key: 'min_players', label: 'Players on', min: 1, max: 249, rule: 'Players on is 1 to 249.' }
] as const;

const whole = (n: unknown, min: number, max: number) => typeof n === 'number' && Number.isInteger(n) && n >= min && n <= max;

/** What is wrong with each vote, by its place in the list ('' for nothing). */
export function voteProblems(list: CustomVote[]): string[] {
	return list.map((v, i) => {
		const name = v.name.trim();
		if (!shortName.test(name)) return shortNameRule;
		if (taken.includes(name) || digits.test(name)) return `/vote ${name} is one of the server's own.`;
		if (repeatsEarlier(list, i, name)) return `Two votes are called ${name}.`;
		const description = v.description.trim();
		if (description && !oneLine(description, 80)) return 'The description is one line of at most 80 bytes.';
		const command = v.command.trim();
		if (!command) return 'Give it a command, such as map {map}.';
		if (!oneLine(command, 320)) return 'The command is one line of at most 320 bytes.';
		if (v.choices.length > maxChoices) return `At most ${maxChoices} choices.`;
		const bad = v.choices.find((c) => !shortName.test(c));
		if (bad !== undefined) return `Choices are 1 to 16 lowercase letters, digits, - or _ (${bad} is not).`;
		const arg = command.includes('{arg}');
		if (arg && !v.choices.length) return 'A command with {arg} needs choices to fill it.';
		if (!arg && v.choices.length) return 'Choices need {arg} in the command, where the choice goes.';
		// A length is 0, the votes' own, or at least 10 s.
		for (const n of voteNumbers) if (!whole(v[n.key], n.min, n.max) || (n.key === 'seconds' && v.seconds > 0 && v.seconds < 10)) return n.rule;
		return '';
	});
}
