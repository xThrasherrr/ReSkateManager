// Text measured the way the manager measures it, so a limit here is the one
// the manager holds to.

const encoder = new TextEncoder();

/** How many bytes s takes as UTF-8: the unit of chat, commands and text settings. */
export const utf8Length = (s: string) => encoder.encode(s).length;

/** How many characters s has, counting each emoji or other astral character once:
 * the unit of server names. */
const chars = (s: string) => [...s].length;

/** Control characters, which the server's console can't take. */
export const control = /\p{Cc}/u;

/** Whether s is one line of at most max bytes, as a chat line or a server command must be. */
export const oneLine = (s: string, max: number) => utf8Length(s) <= max && !control.test(s);

/** Control characters, and those that flip which way text runs, which names can't hold. */
const nameUnfit = /[\p{Cc}\p{Bidi_Control}]/u;

/** The server's short names: a chat command's, a custom vote's or its choices, a park mod's. */
export const shortName = /^[a-z0-9_-]{1,16}$/;
export const shortNameRule = 'The name is 1 to 16 lowercase letters, digits, - or _.';

/** Names made only of digits, which the server reads as numbers. */
export const digits = /^\d+$/;

/** Whether an item before place i in list already has the name. */
export const repeatsEarlier = (list: { name: string }[], i: number, name: string) => list.findIndex((o) => o.name.trim() === name) !== i;

/** A colour as the server writes it, "#RRGGBB", or typed without the #. */
export const hexColour = /^#?[0-9a-f]{6}$/i;

// The patterns are for an input's pattern attribute, kept here since Svelte
// reads the braces of one written in the markup as an expression.

/** A panel username: 3-32 letters, digits and _ . - */
export const usernamePattern = String.raw`[A-Za-z0-9_.\-]{3,32}`;

const steamId = String.raw`7656119[0-9]{10}`;
/** A SteamID64, with spaces around it allowed. */
export const steamIdPattern = String.raw`\s*${steamId}\s*`;
const steamIdOnly = new RegExp(`^${steamId}$`);
/** Whether s, trimmed, is a SteamID64: 17 digits starting 7656119. */
export const isSteamId = (s: string) => steamIdOnly.test(s.trim());

/** What is wrong with a server name for the panel, or '' when nothing is: 1-64
 * characters once trimmed, none of them control or direction characters. */
export function serverNameProblem(name: string): string {
	const s = name.trim();
	if (!s) return 'Give it a name.';
	if (chars(s) > 64) return 'Names are at most 64 characters.';
	if (nameUnfit.test(s)) return "A name can't hold control or text-direction characters.";
	return '';
}
