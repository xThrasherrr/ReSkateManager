// Text measured the way the manager measures it, so a limit here is the one
// the manager holds to.

const encoder = new TextEncoder();

/** How many bytes s takes as UTF-8: the unit of chat, commands and text settings. */
export const utf8Length = (s: string) => encoder.encode(s).length;

/** How many characters s has, counting each emoji or other astral character once:
 * the unit of server names. */
export const chars = (s: string) => [...s].length;

/** Control characters, which the server's console can't take. */
export const control = /\p{Cc}/u;

/** Control characters, and those that flip which way text runs, which names can't hold. */
export const nameUnfit = /[\p{Cc}\p{Bidi_Control}]/u;

/** What is wrong with a server name for the panel, or '' when nothing is: 1-64
 * characters once trimmed, none of them control or direction characters. */
export function serverNameProblem(name: string): string {
	const s = name.trim();
	if (!s) return 'Give it a name.';
	if (chars(s) > 64) return 'Names are at most 64 characters.';
	if (nameUnfit.test(s)) return "A name can't hold control or text-direction characters.";
	return '';
}
