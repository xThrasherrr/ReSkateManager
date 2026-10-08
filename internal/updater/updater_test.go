package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.zip")
	f, _ := os.Create(path)
	w := zip.NewWriter(f)
	for name, body := range files {
		fw, _ := w.Create(name)
		fw.Write([]byte(body))
	}
	w.Close()
	f.Close()
	data, _ := os.ReadFile(path)
	return data
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestDownloadAndInstall(t *testing.T) {
	exe := ExeName()
	zipData := makeZip(t, map[string]string{
		exe:                  "new server",
		"steam_api64.dll":    "dll",
		"ReSkateServer.json": "{\"should\":\"not overwrite\"}",
		"Mods/x/readme.txt":  "no",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/launcher.json":
			fmt.Fprintf(w, `{"schema":1,"server":{"version":"9.9.9","url":"%s/server.zip","sha256":"%s","size":%d,"exe_sha256":"%s"}}`,
				"https://example.invalid", sum(zipData), len(zipData), sum([]byte("new server")))
		case "/server.zip":
			w.Write(zipData)
		}
	}))
	defer srv.Close()

	c := NewChecker("unused")
	c.GOOS = "windows"
	c.LauncherJSON = srv.URL + "/launcher.json"
	r, err := c.Latest(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "9.9.9" {
		t.Fatalf("version %q", r.Version)
	}
	r.URL = srv.URL + "/server.zip" // the manifest must be HTTPS; point the test at the local server

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, exe), []byte("old server"), 0o755)
	os.WriteFile(filepath.Join(dir, "ReSkateServer.json"), []byte("{\"mine\":true}"), 0o644)
	if cur, _ := FileSHA256(filepath.Join(dir, exe)); cur == r.ExeSHA256 {
		t.Fatal("old exe already current")
	}

	zipPath, err := c.Download(context.Background(), r, filepath.Join(dir, "cache"), nil)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := Install(zipPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 {
		t.Errorf("installed %v", installed)
	}
	if cur, _ := FileSHA256(filepath.Join(dir, exe)); cur != r.ExeSHA256 {
		t.Error("exe not replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ReSkateServer.json")); string(b) != "{\"mine\":true}" {
		t.Errorf("config overwritten: %s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "Mods")); err == nil {
		t.Error("Mods written")
	}

	// A tampered cached zip is downloaded again, and a bad download is refused.
	r.SHA256 = sum([]byte("other"))
	if _, err := c.Download(context.Background(), r, filepath.Join(dir, "cache"), nil); err == nil {
		t.Error("mismatched download accepted")
	}
}

func TestInstallRejectsTraversal(t *testing.T) {
	data := makeZip(t, map[string]string{"../evil.txt": "x"})
	path := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(path, data, 0o644)
	if _, err := Install(path, t.TempDir()); err == nil {
		t.Error("traversal accepted")
	}
}

// A zip made on Windows may part folders with backslashes, as ReSkate
// 1.1.8's does: they install as folders, and still can't leave the server's.
func TestInstallBackslashNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "win.zip")
	os.WriteFile(path, makeZip(t, map[string]string{"ReSkateServer.exe": "x", `licenses\lz4-LICENSE.txt`: "lz4"}), 0o644)
	if _, err := Install(path, dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "licenses", "lz4-LICENSE.txt")); err != nil || string(b) != "lz4" {
		t.Errorf("licenses/lz4-LICENSE.txt: %q, %v", b, err)
	}
	for _, bad := range []string{`..\evil.txt`, `licenses\..\..\evil.txt`, `\evil.txt`} {
		os.WriteFile(path, makeZip(t, map[string]string{bad: "x"}), 0o644)
		if _, err := Install(path, t.TempDir()); err == nil {
			t.Errorf("%s installed", bad)
		}
	}
}

type tarEntry struct {
	name, body string
	mode       int64
	link       bool
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case strings.HasSuffix(e.name, "/"):
			h.Typeflag, h.Size = tar.TypeDir, 0
		case e.link:
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.body, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeGitHub serves a latest release whose only server is the given Linux tarball.
func fakeGitHub(t *testing.T, tarball []byte, digest string) (*Checker, *int) {
	t.Helper()
	gets := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[
				{"name":"ReSkateServer-9.9.9.zip","browser_download_url":"%[1]s/win.zip","size":10,"digest":"sha256:%[2]s"},
				{"name":"ReSkateServer-Linux-9.9.9.tar.gz","browser_download_url":"%[1]s/linux.tar.gz","size":%[3]d,"digest":"%[4]s"}]}`,
				srv.URL, strings.Repeat("0", 64), len(tarball), digest)
		case "/linux.tar.gz":
			gets++
			w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewChecker("o/r")
	c.GOOS, c.API = "linux", srv.URL
	return c, &gets
}

func TestLinuxRelease(t *testing.T) {
	tarball := makeTarGz(t, []tarEntry{
		{name: "ReSkateServer-Linux-9.9.9/"},
		{name: "ReSkateServer-Linux-9.9.9/ReSkateServer", body: "new linux server", mode: 0o755},
		{name: "ReSkateServer-Linux-9.9.9/licenses/"},
		{name: "ReSkateServer-Linux-9.9.9/licenses/x-LICENSE.txt", body: "license", mode: 0o644},
		{name: "ReSkateServer-Linux-9.9.9/libsteam_api.so", body: "so", mode: 0o644},
		{name: "ReSkateServer-Linux-9.9.9/setup-linux-server-libs.sh", body: "#!/bin/sh", mode: 0o755},
		{name: "ReSkateServer-Linux-9.9.9/world-layers.json", body: "{}", mode: 0o644},
		{name: "ReSkateServer-Linux-9.9.9/ReSkateServer.json", body: "{\"release\":true}", mode: 0o644},
	})
	c, gets := fakeGitHub(t, tarball, "sha256:"+sum(tarball))
	ctx := context.Background()

	r, err := c.Latest(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "9.9.9" || r.Tag != "v9.9.9" || r.SHA256 != sum(tarball) || r.ExeSHA256 != sum([]byte("new linux server")) {
		t.Fatalf("release %+v", r)
	}
	// The exe hash is remembered per tarball, so later checks do not fetch it again.
	if _, err := c.Latest(ctx, true); err != nil || *gets != 1 {
		t.Fatalf("second check: %v, %d tarball fetches", err, *gets)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ReSkateServer"), []byte("old server"), 0o755)
	os.WriteFile(filepath.Join(dir, "ReSkateServer.json"), []byte("{\"mine\":true}"), 0o644)
	archive, err := c.Download(ctx, r, filepath.Join(dir, "cache"), nil)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := Install(archive, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 5 {
		t.Errorf("installed %v", installed)
	}
	if cur, _ := FileSHA256(filepath.Join(dir, "ReSkateServer")); cur != r.ExeSHA256 {
		t.Error("exe not replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ReSkateServer.json")); string(b) != "{\"mine\":true}" {
		t.Errorf("config overwritten: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "world-layers.json")); string(b) != "{}" {
		t.Error("missing world-layers.json not created")
	}
	if _, err := os.Stat(filepath.Join(dir, "licenses", "x-LICENSE.txt")); err != nil {
		t.Error("wrapper folder not stripped")
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{"ReSkateServer": 0o755, "setup-linux-server-libs.sh": 0o755, "world-layers.json": 0o644} {
			if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.Mode().Perm()&0o111 != want&0o111 {
				t.Errorf("%s mode %v", name, st.Mode())
			}
		}
	}
}

func TestLinuxReleaseNeedsDigest(t *testing.T) {
	tarball := makeTarGz(t, []tarEntry{{name: "ReSkateServer", body: "x", mode: 0o755}})
	for _, digest := range []string{"", "md5:abc", "sha256:nothex"} {
		c, _ := fakeGitHub(t, tarball, digest)
		if _, err := c.Latest(context.Background(), true); err == nil {
			t.Errorf("digest %q accepted", digest)
		}
	}
}

func TestTarInstallRefusesLinksAndTraversal(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"symlink":   {{name: "ReSkateServer", body: "x", mode: 0o755}, {name: "evil", body: "/etc/passwd", link: true}},
		"traversal": {{name: "ReSkateServer", body: "x", mode: 0o755}, {name: "../evil.txt", body: "x", mode: 0o644}},
	} {
		path := filepath.Join(t.TempDir(), "bad.tar.gz")
		os.WriteFile(path, makeTarGz(t, entries), 0o644)
		dir := t.TempDir()
		if _, err := Install(path, dir); err == nil {
			t.Errorf("%s accepted", name)
		}
		if _, err := os.Stat(filepath.Join(dir, "ReSkateServer")); err == nil {
			t.Errorf("%s: files written before the refusal", name)
		}
	}
}

// A file that can't go in puts back the ones that went in before it: the
// server is the old release or the new, never half of each.
func TestInstallRollsBack(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("old b"), 0o644)
	// In the way of setting b.txt aside.
	os.MkdirAll(filepath.Join(dir, "b.txt.update-old", "x"), 0o755)
	zipPath := filepath.Join(t.TempDir(), "release.zip")
	os.WriteFile(zipPath, makeZip(t, map[string]string{"a.txt": "new a", "b.txt": "new b"}), 0o644)

	if _, err := Install(zipPath, dir); err == nil {
		t.Fatal("installed with b.txt stuck")
	}
	for name, want := range map[string]string{"a.txt": "old a", "b.txt": "old b"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.update-new")); len(left) > 0 {
		t.Errorf("staged files left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt.update-old")); !os.IsNotExist(err) {
		t.Errorf("a.txt's old copy left: %v", err)
	}
}

// A zip's names are cleaned before the host's own files are recognised.
func TestInstallKeepsHostFilesWhateverTheirSpelling(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ReSkateServer.json"), []byte(`{"name":"mine"}`), 0o644)
	zipPath := filepath.Join(t.TempDir(), "release.zip")
	os.WriteFile(zipPath, makeZip(t, map[string]string{"./ReSkateServer.json": `{"name":"theirs"}`, "Mods/../x.txt": "x"}), 0o644)
	if _, err := Install(zipPath, dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "ReSkateServer.json")); string(got) != `{"name":"mine"}` {
		t.Errorf("the host's config was replaced: %s", got)
	}
}

func TestLinuxAsset(t *testing.T) {
	one := map[string]asset{"ReSkateServer-Linux-1.2.0.tar.gz": {Name: "ReSkateServer-Linux-1.2.0.tar.gz"}, "launcher.json": {Name: "launcher.json"}}
	if a, err := linuxAsset(one); err != nil || a.Name != "ReSkateServer-Linux-1.2.0.tar.gz" {
		t.Errorf("one: %v %v", a, err)
	}
	two := map[string]asset{
		"ReSkateServer-Linux-arm64-1.2.0.tar.gz": {Name: "ReSkateServer-Linux-arm64-1.2.0.tar.gz"},
		"ReSkateServer-Linux-x64-1.2.0.tar.gz":   {Name: "ReSkateServer-Linux-x64-1.2.0.tar.gz"},
	}
	for range 20 { // map order must not matter
		a, err := linuxAsset(two)
		if err != nil || !strings.Contains(a.Name, map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]) {
			t.Fatalf("two on %s: %v %v", runtime.GOARCH, a, err)
		}
	}
	if _, err := linuxAsset(map[string]asset{}); err == nil {
		t.Error("none")
	}
}

func TestCheckOverride(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://api.github.com":    true,
		"http://127.0.0.1:8080":     true,
		"http://localhost:9000/x":   true,
		"http://[::1]:1/":           true,
		"http://example.com":        false,
		"http://192.168.1.5/launch": false,
		"launcher.json":             false,
		"ftp://example.com/x":       false,
	} {
		if err := CheckOverride(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestParseVersion(t *testing.T) {
	for v, ok := range map[string]bool{"1.2.3": true, "v0.10.0": true, "0.0.0": true, "01.2.3": false, "+1.2.3": false, "1.2": false, "1.2.3-rc.1": true,
		"1.-2.3": false, "dev": false, "1.2.3-rc": false, "1.2.3-rc.01": false, "1.2.3-rc3": false, "1.2.3-beta.1": false, "1.2.3-rc.1+x": false, "v0.9.7-3-gabc123": false} {
		if (parseVersion(v) != nil) != ok {
			t.Errorf("parseVersion(%q)", v)
		}
	}
}
