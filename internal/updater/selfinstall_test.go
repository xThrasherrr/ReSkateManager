package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildFakeManager builds a program that answers --version with product and
// version.
func buildFakeManager(t *testing.T, product, version string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fake.exe")
	flags := "-X 'main.product=" + product + "' -X main.version=" + version
	if b, err := exec.Command("go", "build", "-ldflags", flags, "-o", out, "./testdata/fakemanager").CombinedOutput(); err != nil {
		t.Fatalf("build fake manager: %v\n%s", err, b)
	}
	data, _ := os.ReadFile(out)
	return data
}

func managerArchiveOf(t *testing.T, binary string, bin []byte) []byte {
	if runtime.GOOS == "windows" {
		return makeZip(t, map[string]string{binary: string(bin), "README.md": "readme"})
	}
	return makeTarGz(t, []tarEntry{{name: "README.md", body: "readme", mode: 0o644}, {name: binary, body: string(bin), mode: 0o755}})
}

func TestSelfInstall(t *testing.T) {
	good := buildFakeManager(t, "ReSkateManager", "2.0.0")
	name := managerArchive("2.0.0")
	var archive []byte
	digest, sums := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/m/releases/latest":
			d := ""
			if digest != "" {
				d = `,"digest":"sha256:` + digest + `"`
			}
			w.Write([]byte(`{"tag_name":"v2.0.0","html_url":"x","assets":[` +
				`{"name":"` + name + `","browser_download_url":"http://` + r.Host + `/a"` + d + `},` +
				`{"name":"checksums.txt","browser_download_url":"http://` + r.Host + `/sums"}]}`))
		case "/a":
			w.Write(archive)
		case "/sums":
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	install := func() (string, error) {
		t.Helper()
		// An install keeps the file name it had: this one is from before 0.9.
		exe := filepath.Join(t.TempDir(), "reskate-manager.exe")
		os.WriteFile(exe, []byte("old manager"), 0o755)
		s := &SelfChecker{Repo: "o/m", Current: "1.0.0", API: srv.URL}
		if err := s.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, err := s.Install(context.Background(), exe)
		return exe, err
	}
	content := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	archive = managerArchiveOf(t, managerBinary(), good)
	digest = sum(archive)
	exe, err := install()
	if err != nil {
		t.Fatal(err)
	}
	if content(exe) != string(good) || content(exe+".old") != "old manager" {
		t.Error("binary not swapped")
	}
	keepOld = 0
	RemoveOld(context.Background(), exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Error("old binary left behind")
	}

	// No digest from GitHub: checksums.txt is used instead.
	digest, sums = "", sum(archive)+"  "+name+"\n"
	if _, err := install(); err != nil {
		t.Errorf("checksums.txt: %v", err)
	}

	for what, setup := range map[string]func(){
		"wrong SHA-256": func() { digest = strings.Repeat("0", 64) },
		"no SHA-256":    func() { digest, sums = "", "" },
		"wrong version": func() {
			archive = managerArchiveOf(t, managerBinary(), buildFakeManager(t, "ReSkateManager", "1.9.0"))
			digest = sum(archive)
		},
		"another program": func() {
			archive = managerArchiveOf(t, managerBinary(), buildFakeManager(t, "Other", "2.0.0"))
			digest = sum(archive)
		},
		"no manager in it": func() {
			archive = managerArchiveOf(t, "other", good)
			digest = sum(archive)
		},
	} {
		archive = managerArchiveOf(t, managerBinary(), good)
		digest = sum(archive)
		setup()
		exe, err := install()
		if err == nil {
			t.Errorf("%s: installed", what)
		}
		if content(exe) != "old manager" {
			t.Errorf("%s: the old binary was replaced", what)
		}
		if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
			t.Errorf("%s: left %s.new", what, exe)
		}
	}
}

// A release candidate finds the next one in the release list and installs it.
func TestSelfInstallCandidate(t *testing.T) {
	bin := buildFakeManager(t, "ReSkateManager", "1.0.0-rc.2")
	archive := managerArchiveOf(t, managerBinary(), bin)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/m/releases":
			w.Write([]byte(`[{"tag_name":"v1.0.0-rc.2","html_url":"x","assets":[{"name":"` + managerArchive("1.0.0-rc.2") +
				`","browser_download_url":"http://` + r.Host + `/a","digest":"sha256:` + sum(archive) + `"}]},` +
				`{"tag_name":"v1.0.0-rc.1","html_url":"y"}]`))
		case "/a":
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	exe := filepath.Join(t.TempDir(), "ReSkateManager.exe")
	os.WriteFile(exe, []byte("rc.1"), 0o755)
	s := &SelfChecker{Repo: "o/m", Current: "1.0.0-rc.1", API: srv.URL}
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	rel, err := s.Install(context.Background(), exe)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); rel.Version != "1.0.0-rc.2" || string(b) != string(bin) {
		t.Errorf("installed %q", rel.Version)
	}
}
