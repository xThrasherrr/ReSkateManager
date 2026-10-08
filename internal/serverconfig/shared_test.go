package serverconfig

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// writeMod makes a mod folder at path with a map and, given a version, a
// manifest.
func writeMod(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "reskate-levels.json"), []byte(levels), 0o644)
	if version != "" {
		os.WriteFile(filepath.Join(path, "manifest.json"), []byte(`{"name":"BBCity","author":"sk8r","version_number":"`+version+`"}`), 0o644)
	}
}

// linksTo reports whether path is a link to target.
func linksTo(path, target string) bool { return linkTarget(path) != "" && sameFile(path, target) }

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A server that uses the shared mods gets a link to each, which the server
// reads as a mod folder. Its own copy wins, a link it disabled stays disabled,
// and links go when their mod does or the server stops using the shared mods.
func TestSyncShared(t *testing.T) {
	shared, dir := filepath.Join(t.TempDir(), SharedName), t.TempDir()
	lib := ModsDir(shared)
	writeMod(t, filepath.Join(lib, "bbcity"), "1.0.0")
	writeMod(t, filepath.Join(lib, "park"), "")
	writeMod(t, filepath.Join(lib, "own"), "2.0.0")
	writeMod(t, filepath.Join(dir, "Mods", "own"), "1.0.0")

	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"bbcity", "park"} {
		if !linksTo(filepath.Join(dir, "Mods", m), filepath.Join(lib, m)) {
			t.Errorf("%s is not linked", m)
		}
	}
	if linkTarget(filepath.Join(dir, "Mods", "own")) != "" {
		t.Error("the server's own copy was replaced")
	}
	mods, err := ReadMods(dir)
	if err != nil || len(mods) != 3 {
		t.Fatalf("read back %+v, %v", mods, err)
	}
	for _, m := range mods {
		if m.Shared != (m.Folder != "own") || len(m.Maps) != 1 {
			t.Errorf("%s: %+v", m.Folder, m)
		}
		if m.Folder == "bbcity" && m.Version != "1.0.0" {
			t.Errorf("bbcity's manifest was not read through the link: %+v", m)
		}
	}

	// Disabled on this server: the link moves, and syncing leaves it there.
	if err := SetModEnabled(dir, "park", false); err != nil {
		t.Fatal(err)
	}
	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "Mods", "park")) || !linksTo(filepath.Join(dir, "DisabledMods", "park"), filepath.Join(lib, "park")) {
		t.Error("a disabled shared mod came back on")
	}

	// A shared mod's link can't be overwritten by an upload to the server.
	if _, _, err := InstallMod(dir, writeZip(t, "reskate-levels.json", levels), "bbcity.zip"); err == nil {
		t.Error("an upload replaced a shared mod's link")
	}

	// Deleting it from the server removes the link, never the shared copy.
	if err := DeleteMod(dir, "bbcity"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "Mods", "bbcity")) || !exists(filepath.Join(lib, "bbcity", "reskate-levels.json")) {
		t.Error("deleting the link did not go as it should")
	}

	// A mod gone from the shared folder loses its links, disabled or not.
	if err := DeleteMod(shared, "park"); err != nil {
		t.Fatal(err)
	}
	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "DisabledMods", "park")) {
		t.Error("the link to a deleted shared mod stayed")
	}

	// Turned off, every link goes and the shared mods stay.
	if err := SyncShared(dir, shared, SharedOff); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "Mods", "bbcity")) || !exists(filepath.Join(dir, "Mods", "own", "manifest.json")) {
		t.Error("turning the shared mods off left a link or took the server's own mod")
	}
	if !exists(filepath.Join(lib, "bbcity", "reskate-levels.json")) {
		t.Error("turning the shared mods off deleted a shared mod")
	}
}

// After the manager's folder moves, links that lead to the old shared folder,
// or nowhere, are pointed at the new one. Links of the server's own are left
// alone.
func TestSyncSharedRepairsMovedLinks(t *testing.T) {
	old, now, dir := filepath.Join(t.TempDir(), SharedName), filepath.Join(t.TempDir(), SharedName), t.TempDir()
	writeMod(t, filepath.Join(ModsDir(old), "bbcity"), "1.0.0")
	writeMod(t, filepath.Join(ModsDir(now), "bbcity"), "1.0.0")
	writeMod(t, filepath.Join(ModsDir(now), "park"), "")
	elsewhere := filepath.Join(t.TempDir(), "maps", "mine")
	writeMod(t, elsewhere, "")
	os.MkdirAll(filepath.Join(dir, "Mods"), 0o755)
	os.MkdirAll(filepath.Join(dir, "DisabledMods"), 0o755)
	for _, l := range [][2]string{
		{filepath.Join(ModsDir(old), "bbcity"), filepath.Join(dir, "Mods", "bbcity")},
		{filepath.Join(ModsDir(old), "park"), filepath.Join(dir, "DisabledMods", "park")}, // leads nowhere
		{elsewhere, filepath.Join(dir, "Mods", "mine")},
	} {
		if err := makeLink(l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := SyncShared(dir, now, SharedAll); err != nil {
		t.Fatal(err)
	}
	if !linksTo(filepath.Join(dir, "Mods", "bbcity"), filepath.Join(ModsDir(now), "bbcity")) {
		t.Error("a link to the old shared folder was not moved")
	}
	if !linksTo(filepath.Join(dir, "DisabledMods", "park"), filepath.Join(ModsDir(now), "park")) || exists(filepath.Join(dir, "Mods", "park")) {
		t.Error("a broken disabled link was not repaired in place")
	}
	if !linksTo(filepath.Join(dir, "Mods", "mine"), elsewhere) {
		t.Error("the server's own link was changed")
	}
	mods, _ := ReadMods(dir)
	i := slices.IndexFunc(mods, func(m Mod) bool { return m.Folder == "mine" })
	if i < 0 || mods[i].Shared {
		t.Errorf("the server's own link: %+v", mods)
	}

	if err := SyncShared(dir, now, SharedOff); err != nil {
		t.Fatal(err)
	}
	if !linksTo(filepath.Join(dir, "Mods", "mine"), elsewhere) {
		t.Error("turning the shared mods off removed the server's own link")
	}
}

// Syncs of one server may overlap, as a start does with a change made in the
// panel; neither takes the other's link for an error.
func TestSyncSharedOverlaps(t *testing.T) {
	shared, dir := filepath.Join(t.TempDir(), SharedName), t.TempDir()
	for _, m := range []string{"a", "b", "c", "d"} {
		writeMod(t, filepath.Join(ModsDir(shared), m), "")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- SyncShared(dir, shared, SharedAll) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	for _, m := range []string{"a", "b", "c", "d"} {
		if !linksTo(filepath.Join(dir, "Mods", m), filepath.Join(ModsDir(shared), m)) {
			t.Errorf("%s is not linked", m)
		}
	}
}

// A server that picks its shared mods gets new ones disabled, and keeps the
// ones it turned on.
func TestSyncSharedPick(t *testing.T) {
	shared, dir := filepath.Join(t.TempDir(), SharedName), t.TempDir()
	writeMod(t, filepath.Join(ModsDir(shared), "bbcity"), "1.0.0")
	if err := SyncShared(dir, shared, SharedPick); err != nil {
		t.Fatal(err)
	}
	if !linksTo(filepath.Join(dir, "DisabledMods", "bbcity"), filepath.Join(ModsDir(shared), "bbcity")) || exists(filepath.Join(dir, "Mods", "bbcity")) {
		t.Fatal("a picked server loaded a shared mod by itself")
	}
	if err := SetModEnabled(dir, "bbcity", true); err != nil {
		t.Fatal(err)
	}
	writeMod(t, filepath.Join(ModsDir(shared), "park"), "")
	if err := SyncShared(dir, shared, SharedPick); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, "Mods", "bbcity")) || !exists(filepath.Join(dir, "DisabledMods", "park")) || exists(filepath.Join(dir, "Mods", "park")) {
		t.Error("the enabled mod moved, or the new one loaded")
	}
}

// Deleting a server's folder takes its links, not the shared mods.
func TestRemoveServerKeepsShared(t *testing.T) {
	shared, dir := filepath.Join(t.TempDir(), SharedName), t.TempDir()
	writeMod(t, filepath.Join(ModsDir(shared), "bbcity"), "1.0.0")
	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(ModsDir(shared), "bbcity", "reskate-levels.json")) {
		t.Fatal("removing a server's folder deleted a shared mod")
	}
	// Syncing a server that is gone does not bring its folder back.
	if err := SyncShared(dir, shared, SharedAll); err != nil || exists(dir) {
		t.Fatalf("synced a deleted server: %v", err)
	}
}

// Moving a mod to the shared folder links it back where it was; other
// servers' copies give way to it only when they are the same version.
func TestShareMod(t *testing.T) {
	shared, a, b, c := filepath.Join(t.TempDir(), SharedName), t.TempDir(), t.TempDir(), t.TempDir()
	writeMod(t, filepath.Join(a, "DisabledMods", "bbcity"), "1.0.0")
	writeMod(t, filepath.Join(b, "Mods", "bbcity"), "1.0.0")
	writeMod(t, filepath.Join(c, "Mods", "bbcity"), "1.1.0")

	if err := ShareMod(a, "bbcity", shared); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(ModsDir(shared), "bbcity")
	if !exists(filepath.Join(lib, "manifest.json")) || !linksTo(filepath.Join(a, "DisabledMods", "bbcity"), lib) {
		t.Fatal("not moved and linked back in place")
	}
	if err := ShareMod(a, "bbcity", shared); err == nil {
		t.Error("shared a link")
	}

	if ok, err := UseShared(b, "bbcity", shared); !ok || err != nil || !linksTo(filepath.Join(b, "Mods", "bbcity"), lib) {
		t.Errorf("same version: %v, %v", ok, err)
	}
	if left, _ := filepath.Glob(filepath.Join(b, ".mod-*")); len(left) > 0 {
		t.Errorf("left behind %v", left)
	}
	if ok, err := UseShared(c, "bbcity", shared); ok || err != nil || linkTarget(filepath.Join(c, "Mods", "bbcity")) != "" {
		t.Errorf("another version: %v, %v", ok, err)
	}
	// Moving a different version over the shared one is refused.
	if err := ShareMod(c, "bbcity", shared); err == nil {
		t.Error("a different version replaced the shared one")
	}

	// Without a version, the same files at the same sizes are the same mod.
	writeMod(t, filepath.Join(ModsDir(shared), "park"), "")
	writeMod(t, filepath.Join(b, "Mods", "park"), "")
	writeMod(t, filepath.Join(c, "Mods", "park"), "")
	os.WriteFile(filepath.Join(c, "Mods", "park", "extra.bin"), []byte("x"), 0o644)
	if ok, _ := UseShared(b, "park", shared); !ok {
		t.Error("an identical copy without a version was kept")
	}
	if ok, _ := UseShared(c, "park", shared); ok {
		t.Error("a copy with other files was swapped")
	}
}

// An update that lands in a new folder takes each server's link with it.
func TestRenameShared(t *testing.T) {
	shared, dir := filepath.Join(t.TempDir(), SharedName), t.TempDir()
	writeMod(t, filepath.Join(ModsDir(shared), "Sk8r-BBCity-1.0.0"), "1.0.0")
	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	if err := SetModEnabled(dir, "Sk8r-BBCity-1.0.0", false); err != nil {
		t.Fatal(err)
	}
	os.Rename(filepath.Join(ModsDir(shared), "Sk8r-BBCity-1.0.0"), filepath.Join(ModsDir(shared), "Sk8r-BBCity-1.1.0"))
	if err := RenameShared(dir, "Sk8r-BBCity-1.0.0", "Sk8r-BBCity-1.1.0", shared); err != nil {
		t.Fatal(err)
	}
	if err := SyncShared(dir, shared, SharedAll); err != nil {
		t.Fatal(err)
	}
	if !linksTo(filepath.Join(dir, "DisabledMods", "Sk8r-BBCity-1.1.0"), filepath.Join(ModsDir(shared), "Sk8r-BBCity-1.1.0")) ||
		exists(filepath.Join(dir, "DisabledMods", "Sk8r-BBCity-1.0.0")) || exists(filepath.Join(dir, "Mods", "Sk8r-BBCity-1.1.0")) {
		t.Error("the link did not follow the update, still disabled")
	}
}
