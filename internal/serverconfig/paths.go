package serverconfig

import "slices"

// Paths maps where a server from ReSkate 1.1.7 on keeps each setting the
// manager reads or writes, dotted ("players.allow_voice_chat"), to its flat
// key, so a newer server's own config can be checked against what the
// manager knows (cmd/reskate-watch).
func Paths() map[string]string {
	out := map[string]string{}
	for _, fd := range Fields {
		if !slices.Contains(Removed, fd.Key) {
			out[places(fd.Key)[0]] = fd.Key
		}
	}
	for key := range sections {
		if p := places(key)[0]; out[p] == "" {
			out[p] = key
		}
	}
	return out
}
