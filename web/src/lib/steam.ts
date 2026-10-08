// The Steam sign-in sends the browser back with ?error=<code> when it fails.
// Only these words are shown, never text from the address bar, which anyone
// could put in a link to the panel.
const messages: Partial<Record<string, string>> = {
	expired: 'The Steam sign-in expired or was started in another browser. Try again.',
	cancelled: 'Steam sign-in was cancelled.',
	unreachable: "Couldn't reach Steam to confirm the sign-in. Try again in a moment.",
	stale: 'That Steam sign-in was already used or is too old. Try again.',
	rejected: "Steam didn't confirm the sign-in. Try again.",
	'too-many': 'Too many failed sign-ins from here. Wait a few minutes, then try again.',
	'no-access': "That Steam account isn't linked to anyone on this panel. Sign in with your password and link it on your Account page, or ask an owner.",
	'sign-in-first': 'Sign in before linking a Steam account.',
	taken: 'That Steam account is already linked to someone else on this panel.',
	'no-address': "Steam sign-in needs the panel's address. An owner can set it on the Manager page."
};
const failed = "Steam sign-in didn't work. Try again.";

/** The message for a Steam sign-in error code, or '' for none. */
export function steamError(code: string | null): string {
	if (!code) return '';
	return messages[code] ?? failed;
}
