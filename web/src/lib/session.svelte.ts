import { api, type Meta, type Perm, type Perms, type User } from './api';

interface Me {
	/** Only for a signed-in user. */
	version?: string;
	setupRequired: boolean;
	/** Whether Steam sign-in works at the address the panel was opened at. */
	steam: boolean;
	user?: User;
	perms?: Perms;
}

class Session {
	loaded = $state(false);
	version = $state('');
	setupRequired = $state(false);
	steam = $state(false);
	user = $state.raw<User | null>(null);
	perms = $state.raw<Perms | null>(null);
	meta = $state.raw<Meta | null>(null);

	async refresh() {
		const me = await api.get<Me>('/auth/me');
		this.version = me.version ?? '';
		this.setupRequired = me.setupRequired;
		this.steam = me.steam;
		this.user = me.user ?? null;
		this.perms = me.perms ?? null;
		if (this.user && !this.meta) this.meta = await api.get<Meta>('/meta');
		this.loaded = true;
	}

	/** Whether the user holds perm globally (or, with an instance, on that instance). */
	can(perm: Perm, instance?: string): boolean {
		const p = this.perms;
		if (!p) return false;
		if (p.owner || p.global[perm]) return true;
		if (!instance) return false;
		return !!p.byInstance[instance]?.[perm];
	}

	/** Whether the user holds perm on any instance. */
	canAny(perm: Perm): boolean {
		const p = this.perms;
		if (!p) return false;
		return p.owner || !!p.global[perm] || Object.values(p.byInstance).some((set) => !!set[perm]);
	}

	/** Signs out, then loads the sign-in page afresh, so nothing the last user
	 * saw stays in memory for the next one. */
	async logout() {
		try {
			await api.post('/auth/logout');
		} finally {
			forgetHistory();
			location.assign('/login');
		}
	}
}

export const session = new Session();

/** Drops the console commands this browser remembers, which are the last
 * user's, not the next one's. */
function forgetHistory() {
	try {
		for (const key of Object.keys(localStorage)) if (key.startsWith('rsm.history.')) localStorage.removeItem(key);
	} catch {
		/* storage may be unavailable */
	}
}
