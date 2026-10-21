package plugins

import (
	"fmt"
	"maps"
	"slices"
)

// A plugin that takes others, with takes in its plugin.toml, replaces them:
// as it starts, before its store first loads, each one's store moves into
// its own, the keys with the prefix given, and its boards become this one's.
// A plugin that was removed leaves its store and boards behind, so what it
// kept, such as a count, carries on in the one that took it.

// takingLocked is why a plugin that takes others can't start: one of them is
// still installed, and the two would keep what one of them should.
func (m *Manager) takingLocked(man Manifest) string {
	for _, id := range slices.Sorted(maps.Keys(man.Takes)) {
		if _, ok := m.plugins[id]; ok {
			return fmt.Sprintf("It takes over from %s, which is still installed: remove %s, then turn this one on.", id, id)
		}
	}
	return ""
}

// take moves the stores of the plugins r's takes into its own. One that
// can't move stays where it was, for the next start to try again.
func (r *runner) take() {
	for _, from := range slices.Sorted(maps.Keys(r.man.Takes)) {
		n, err := r.m.data.take(from, r.p.ID, r.man.Takes[from])
		switch {
		case err != nil:
			r.log("warn", from+"'s store didn't move into this one, and stays as it was: "+err.Error())
		case n > 0:
			r.log("info", fmt.Sprintf("Took %s's store: %d keys.", from, n))
		}
	}
}

// take moves plugin from's store into to's, each key with prefix before it,
// and its boards, and is how many keys moved. Neither may be running. What
// either has waiting is written first, and both are read afresh after.
func (d *data) take(from, to, prefix string) (int, error) {
	d.mu.Lock()
	_, a := d.stores[from]
	_, b := d.stores[to]
	d.mu.Unlock()
	// One that ran here is loaded, and only that one has boards not written yet.
	if a || b {
		if err := d.flush(d.ctx, ""); err != nil {
			return 0, err
		}
		d.mu.Lock()
		delete(d.stores, from)
		delete(d.stores, to)
		d.mu.Unlock()
	}
	vals, err := d.st.PluginStore(d.ctx, from)
	if err != nil {
		return 0, err
	}
	for k := range vals {
		if len(prefix)+len(k) > maxKey { // store.get couldn't reach it
			return 0, fmt.Errorf("its key %q would be longer than %d bytes with the prefix", k, maxKey)
		}
	}
	n, err := d.st.MovePluginStore(d.ctx, from, to, prefix)
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	for id, bd := range d.boards {
		if bd.Plugin == from {
			bd.Plugin = to
			d.boards[id] = bd
		}
	}
	d.mu.Unlock()
	return n, nil
}
