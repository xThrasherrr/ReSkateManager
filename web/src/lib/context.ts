import { createContext } from 'svelte';
import type { Perm } from './api';
import type { Live } from './live.svelte';

export interface LiveContext {
	readonly live: Live;
	readonly id: string;
	can(perm: Perm): boolean;
}

export const [useLive, setLive] = createContext<LiveContext>();
