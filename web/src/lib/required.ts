// The plugins players must have to join (ReSkate 2.1.2's plugins.required),
// as the manager keeps them: for every server, and for each server besides.
// Each entry is a Thunderstore package, Owner-Name, or Owner-Name-1.2.3 for
// at least that version.

/** An entry's package in lower case, without the least version it may ask for. */
export const packageOf = (s: string) => s.replace(/-\d+\.\d+\.\d+$/, '').toLowerCase();

/** Whether list requires the package pkg (Owner-Name). */
export const requires = (list: string[] | undefined, pkg: string) => !!list?.some((r) => packageOf(r) === packageOf(pkg));

/** list with pkg put in or taken out. */
export const withRequired = (list: string[], pkg: string, on: boolean) => [...list.filter((r) => packageOf(r) !== packageOf(pkg)), ...(on ? [pkg] : [])];

/** The Thunderstore category ReSkate takes plugins from: a server asks players only for these. */
export const PLUGINS = 'Plugins';
