package serverconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// From ReSkate's plugins release, a server runs the server side of Luau
// plugins from its Plugins folder, a mod folder each, and loads, reloads and
// unloads them as the folder changes (Server/server_plugins.cpp). A plugin
// is a mod like any other here: it's installed, updated, shared, disabled and
// backed up in Mods, and the server reaches each enabled one with a plugin
// for it through a link in Plugins.

// PluginsDir is where the server looks for its plugins.
func PluginsDir(dir string) string { return filepath.Join(dir, "Plugins") }

// SyncPlugins links each enabled mod in dir/Mods that has a server plugin
// into Plugins, and removes the links to mods that no longer have one. What
// else is in Plugins is the owner's, and a folder of theirs keeps out a mod
// of the same name.
func SyncPlugins(dir string) error {
	mods, err := ReadMods(dir)
	if err != nil {
		return err
	}
	want := map[string]string{} // by fold, as the mod's folder spells it
	for _, m := range mods {
		if m.ServerPlugin && !m.Disabled {
			want[fold(m.Folder)] = m.Folder
		}
	}
	lib, err := filepath.Abs(ModsDir(dir)) // a junction needs an absolute target
	if err != nil {
		return err
	}
	plugins := PluginsDir(dir)
	entries, err := os.ReadDir(plugins)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var errs []error
	for _, e := range entries {
		p := filepath.Join(plugins, e.Name())
		key := fold(e.Name())
		name, ok := want[key]
		if !isModLink(p) {
			delete(want, key)
			continue
		}
		if ok && sameFile(p, filepath.Join(lib, name)) {
			delete(want, key)
			continue
		}
		errs = append(errs, unlink(p))
	}
	if len(want) > 0 {
		if err := os.MkdirAll(plugins, 0o755); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	for _, name := range want {
		errs = append(errs, link(filepath.Join(lib, name), filepath.Join(plugins, name)))
	}
	return errors.Join(errs...)
}

// isModLink reports whether path is a link to the mod of its name in a Mods
// folder, as SyncPlugins makes. It's still known after the manager's folder
// moves, and then points at the old place until SyncPlugins fixes it.
func isModLink(path string) bool {
	t := linkTarget(path)
	if t == "" {
		return false
	}
	t = filepath.Clean(t)
	return fold(filepath.Base(t)) == fold(filepath.Base(path)) && fold(filepath.Base(filepath.Dir(t))) == fold("Mods")
}
