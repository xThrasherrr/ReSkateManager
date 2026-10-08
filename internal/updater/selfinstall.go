package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var installMu sync.Mutex

// managerArchive is the release archive GoReleaser names for this platform.
func managerArchive(version string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("ReSkateManager-%s-%s-%s%s", version, runtime.GOOS, runtime.GOARCH, ext)
}

// managerBinary is the manager's file name in a release archive.
func managerBinary() string {
	if runtime.GOOS == "windows" {
		return "ReSkateManager.exe"
	}
	return "ReSkateManager"
}

// Install downloads the newer release, checks it against the SHA-256 GitHub
// recorded for the archive (or the release's checksums.txt), and swaps it in
// for exe. The old binary stays as exe+".old" until RemoveOld. The running
// manager carries on as before; the new one takes over when it restarts.
func (s *SelfChecker) Install(ctx context.Context, exe string) (*ManagerRelease, error) {
	if !installMu.TryLock() {
		return nil, errors.New("an update is already being installed")
	}
	defer installMu.Unlock()
	rel := s.Newer()
	if rel == nil {
		return nil, errors.New("there is no newer manager release")
	}
	name := managerArchive(rel.Version)
	a, ok := rel.assets[name]
	if !ok {
		return nil, fmt.Errorf("the release has no %s", name)
	}
	want, ok := strings.CutPrefix(a.Digest, "sha256:")
	if !ok || !sha256Re.MatchString(want) {
		var err error
		if want, err = s.checksum(ctx, rel.assets["checksums.txt"], name); err != nil {
			return nil, err
		}
	}
	dir := filepath.Dir(exe)
	archive := filepath.Join(dir, ".update-"+name)
	defer os.Remove(archive)
	if err := s.download(ctx, a.URL, archive, want); err != nil {
		return nil, err
	}
	next := exe + ".new"
	if err := extractBinary(archive, next); err != nil {
		os.Remove(next)
		return nil, err
	}
	// Run it once: a binary that can't start here must not replace one that can.
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(vctx, next, "--version")
	cmd.WaitDelay = 5 * time.Second // a child it left holding the pipe can't stall this
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "ReSkateManager "+rel.Version {
		os.Remove(next)
		return nil, fmt.Errorf("the new manager did not start (%q, %v)", strings.TrimSpace(string(out)), err)
	}
	old := exe + ".old"
	os.Remove(old)
	// Windows lets a running exe be renamed, though not overwritten.
	if err := os.Rename(exe, old); err != nil {
		os.Remove(next)
		return nil, fmt.Errorf("cannot move the running manager aside: %w", err)
	}
	if err := os.Rename(next, exe); err != nil {
		os.Rename(old, exe)
		os.Remove(next)
		return nil, fmt.Errorf("cannot put the new manager in place: %w", err)
	}
	return rel, nil
}

// keepOld is how long the new manager must run before RemoveOld deletes the
// binary it replaced; a variable so tests needn't wait.
var keepOld = 10 * time.Minute

// RemoveOld deletes the binary a previous Install replaced, once the new one
// has run for keepOld: until then it is there to go back to by hand. On
// Windows the old process may still be exiting, so it retries for a while.
func RemoveOld(ctx context.Context, exe string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(keepOld):
	}
	old := exe + ".old"
	for range 30 {
		if err := os.Remove(old); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(time.Second)
	}
}

func (s *SelfChecker) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (s *SelfChecker) open(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ReSkateManager")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// checksum finds name's SHA-256 in a GoReleaser checksums.txt.
func (s *SelfChecker) checksum(ctx context.Context, sums asset, name string) (string, error) {
	if sums.URL == "" {
		return "", errors.New("the release has no SHA-256 for " + name)
	}
	body, err := s.open(ctx, sums.URL)
	if err != nil {
		return "", err
	}
	defer body.Close()
	sc := bufio.NewScanner(io.LimitReader(body, 1<<20))
	for sc.Scan() {
		if sum, file, ok := strings.Cut(sc.Text(), "  "); ok && file == name && sha256Re.MatchString(sum) {
			return sum, nil
		}
	}
	return "", errors.New("checksums.txt has no SHA-256 for " + name)
}

func (s *SelfChecker) download(ctx context.Context, url, to, want string) error {
	body, err := s.open(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.Create(to)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(body, maxDownload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return errors.New("the download does not match the release's SHA-256")
	}
	return nil
}

func extractBinary(archive, to string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	err = walk(f, archiveKind(archive), func(name string, _ os.FileMode, body io.Reader) error {
		if path.Clean(name) != managerBinary() {
			return nil
		}
		os.Remove(to)
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(body, maxDownload))
		if err == nil {
			err = out.Sync() // on disk before it replaces the running manager
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return errFound
	})
	if errors.Is(err, errFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("the release archive has no " + managerBinary())
}
