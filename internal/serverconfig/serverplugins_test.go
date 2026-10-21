package serverconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlugin(t *testing.T, path, manifest string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "reskate-plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The server reaches each enabled mod with a server plugin through a link in
// Plugins, a shared one too, while a client plugin and a map mod stay out,
// and the owner's own folder in Plugins wins.
func TestSyncPlugins(t *testing.T) {
	shared, dir := filepath.Join(t.TempDir(), SharedName), t.TempDir()
	mods, plugins := ModsDir(dir), PluginsDir(dir)
	writePlugin(t, filepath.Join(mods, "referee"), `{"api": 1, "entry": "plugin/main.lua", "side": "both"}`)
	writePlugin(t, filepath.Join(mods, "speedo"), `{"api": 1, "entry": "plugin/main.lua"}`)
	writePlugin(t, filepath.Join(mods, "broken"), `{"side": `)
	writePlugin(t, filepath.Join(mods, "own"), `{"side": "server"}`)
	writePlugin(t, filepath.Join(plugins, "own"), `{"side": "server"}`)
	writeMod(t, filepath.Join(mods, "bbcity"), "")
	writePlugin(t, filepath.Join(ModsDir(shared), "tag"), `{"side": "server"}`)

	// SyncShared links the shared mods, then their plugins.
	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"referee", "broken", "tag"} {
		if !linksTo(filepath.Join(plugins, m), filepath.Join(mods, m)) {
			t.Errorf("%s is not linked into Plugins", m)
		}
	}
	if _, err := os.Stat(filepath.Join(plugins, "tag", "reskate-plugin.json")); err != nil {
		t.Errorf("the shared plugin can't be read through both links: %v", err)
	}
	for _, m := range []string{"speedo", "bbcity"} {
		if exists(filepath.Join(plugins, m)) {
			t.Errorf("%s was linked into Plugins", m)
		}
	}
	if linkTarget(filepath.Join(plugins, "own")) != "" {
		t.Error("the owner's own plugin was replaced")
	}
	list, err := ReadMods(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		want := m.Folder != "speedo" && m.Folder != "bbcity"
		if m.ServerPlugin != want {
			t.Errorf("%s: server plugin %v, want %v", m.Folder, m.ServerPlugin, want)
		}
	}

	// Disabled, it goes; enabled again, it's back; deleted, it goes for good.
	if err := SetModEnabled(dir, "referee", false); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(plugins, "referee")) {
		t.Error("a disabled mod's plugin is still linked")
	}
	if err := SetModEnabled(dir, "referee", true); err != nil {
		t.Fatal(err)
	}
	if !linksTo(filepath.Join(plugins, "referee"), filepath.Join(mods, "referee")) {
		t.Error("an enabled mod's plugin isn't linked again")
	}
	if err := DeleteMod(dir, "broken"); err != nil {
		t.Fatal(err)
	}
	if err := SyncPlugins(dir); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(plugins, "broken")) {
		t.Error("a deleted mod's plugin is still linked")
	}
	if !exists(filepath.Join(plugins, "own", "reskate-plugin.json")) {
		t.Error("the owner's own plugin went")
	}
}
