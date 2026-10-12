import type { RoleList, ServerRole } from './api';

/** What a server role may use, in short; the whole of it goes in a hover. */
export function canUse(r: ServerRole) {
	const extra = r.commands.filter((c) => c !== '*');
	if (r.commands.includes('*')) return extra.length ? `every command, and ${extra.join(', ')}` : 'every command';
	if (!r.commands.length) return 'no commands';
	return r.commands.length > 5 ? `${r.commands.slice(0, 5).join(', ')} and ${r.commands.length - 5} more` : r.commands.join(', ');
}

/** The whole of what a role may use, for the hover over canUse. */
export const allItCanUse = (r: ServerRole) =>
	[`Commands: ${r.commands.join(', ') || 'none'}`, `Permissions: ${r.permissions.join(', ') || 'none'}`, r.inherit ? '' : 'Only what it lists itself, not the default role’s']
		.filter(Boolean)
		.join('\n');

/** The roles to give: not the default, which everyone without one has already. */
export const giveable = (roles: RoleList | null) => (roles?.roles ?? []).filter((r) => r.name !== roles?.default);

/** The role a player was given, if any. */
export const roleOf = (roles: RoleList | null, id: string) => roles?.roles.find((r) => r.players.some((p) => p.id === id));

/** A player given admin because a panel user's Steam account is theirs: the sync's to change. */
export const panelAdmin = (roles: RoleList | null, id: string) => roles?.roles.some((r) => r.players.some((p) => p.id === id && p.panel)) ?? false;

// ReSkate 2.0.6's limits on a role (README-roles.txt); the manager checks the rest.
export const validRoleName = (s: string) => /^[a-z0-9_-]{1,24}$/.test(s) && s !== 'none';
export const maxRoles = 32;
export const maxRoleTag = 16; // bytes
export const maxRoleRank = 1000;
