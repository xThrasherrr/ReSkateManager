package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/safezip"
)

// What an uploaded plugin may hold. A plugin is a few small Lua files.
const (
	MaxUpload   = 4 << 20  // bytes of zip
	maxFiles    = 256      // files in it
	maxUnpacked = 16 << 20 // bytes they unpack to
)

// ErrBadUpload is a zip that isn't a plugin this manager can take.
var ErrBadUpload = errors.New("not a plugin")

// ExistsError is an upload of a plugin that's there already, which only
// replacing it takes.
type ExistsError struct{ Plugin Status }

func (e *ExistsError) Error() string {
	s := e.Plugin
	what := s.Name
	if s.Version != "" {
		what += " " + s.Version
	}
	return fmt.Sprintf("%s is installed already, as %s", what, s.ID)
}

func badUpload(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadUpload, fmt.Sprintf(format, a...))
}

// Install adds a plugin from a zip of its folder: plugin.toml and main.lua
// at the zip's root, or in the one folder there, which then names the
// plugin; otherwise name, the zip's file name, does. A plugin that's there
// already is replaced only when replace says so, and runs again with its new
// files if it was on; its grants stay, but what it newly asks for waits to
// be granted. A new plugin starts off.
func (m *Manager) Install(data []byte, name string, replace bool) (st Status, replaced bool, err error) {
	if len(data) > MaxUpload {
		return Status{}, false, badUpload("a plugin's zip can be %d MB at most", MaxUpload>>20)
	}
	zr, err := safezip.NewReader(bytes.NewReader(data), int64(len(data)), 1<<20)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return Status{}, false, badUpload("it isn't a zip that can be read: %v", err)
	}
	files, id, err := pluginFiles(zr, name)
	if err != nil {
		return Status{}, false, err
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return Status{}, false, err
	}
	tmp := filepath.Join(m.Dir, ".upload-"+randomName())
	defer os.RemoveAll(tmp) // gone already once it's in place
	if err := unpack(tmp, files); err != nil {
		return Status{}, false, err
	}
	man, _, err := readManifest(tmp)
	if err != nil {
		return Status{}, false, badUpload("%v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Status{}, false, errors.New("the manager is stopping")
	}
	m.scanLocked()
	var p *Plugin
	for _, q := range m.plugins {
		if strings.EqualFold(q.ID, id) { // one folder, whatever the case, on Windows
			p = q
		}
	}
	if p == nil {
		if err := os.Rename(tmp, filepath.Join(m.Dir, id)); err != nil {
			return Status{}, false, err
		}
		m.scanLocked()
		p = m.plugins[id]
		if p == nil {
			return Status{}, false, fmt.Errorf("%s was unpacked but isn't there", id)
		}
		m.note(p, "info", "Added "+version(man)+" from an upload.")
		return m.status(p), false, nil
	}
	if !replace {
		return Status{}, false, &ExistsError{Plugin: m.status(p)}
	}
	m.stopLocked(p, "")
	old := filepath.Join(m.Dir, ".old-"+randomName())
	if err := os.Rename(p.dir, old); err != nil {
		m.restartLocked(p)
		return Status{}, false, fmt.Errorf("its folder couldn't be moved aside: %w", err)
	}
	if err := os.Rename(tmp, p.dir); err != nil {
		os.Rename(old, p.dir)
		m.restartLocked(p)
		return Status{}, false, err
	}
	if err := os.RemoveAll(old); err != nil {
		m.Log.Warn("plugins: the replaced files stay", "dir", old, "err", err)
	}
	p.clearStats()
	m.readLocked(p)
	m.note(p, "info", "Replaced with "+version(man)+" from an upload.")
	m.restartLocked(p)
	return m.status(p), true, nil
}

// Remove stops a plugin and deletes its folder, what it was granted and
// whether it was on.
func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanLocked()
	p, ok := m.plugins[id]
	if !ok {
		return ErrNotFound
	}
	m.stopLocked(p, "")
	if err := os.RemoveAll(p.dir); err != nil {
		m.restartLocked(p)
		return err
	}
	delete(m.plugins, id)
	m.write(id, "info", "Removed.")
	if m.enabled[id] {
		delete(m.enabled, id)
		ids := []string{}
		for k := range m.enabled {
			ids = append(ids, k)
		}
		slices.Sort(ids)
		b, _ := json.Marshal(ids)
		if err := m.Store.Set(ctx, enabledKey, string(b)); err != nil {
			return err
		}
	}
	if cur := m.grants.Load(); cur != nil && len((*cur)[id]) > 0 {
		next := map[string][]string{}
		for k, v := range *cur {
			if k != id {
				next[k] = v
			}
		}
		b, _ := json.Marshal(next)
		if err := m.Store.Set(ctx, grantsKey, string(b)); err != nil {
			return err
		}
		m.grants.Store(&next)
	}
	return nil
}

// cleanUploads removes what an upload cut off by a crash left in Dir.
func (m *Manager) cleanUploads() {
	entries, _ := os.ReadDir(m.Dir)
	for _, e := range entries {
		if n := e.Name(); e.IsDir() && (strings.HasPrefix(n, ".upload-") || strings.HasPrefix(n, ".old-")) {
			os.RemoveAll(filepath.Join(m.Dir, n))
		}
	}
}

// restartLocked starts a plugin again if it's on.
func (m *Manager) restartLocked(p *Plugin) {
	if m.enabled[p.ID] {
		m.startLocked(p)
	}
}

// note puts a line on a plugin's card and in plugins.log.
func (m *Manager) note(p *Plugin, level, text string) {
	p.line(level, text)
	m.write(p.ID, level, text)
}

func version(man Manifest) string {
	if man.Version == "" {
		return "a version with no number"
	}
	return "version " + man.Version
}

func randomName() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// pluginFiles is what of a zip goes in the plugin's folder, by the path it
// has there, and the plugin's ID.
func pluginFiles(zr *zip.Reader, name string) (map[string]*zip.File, string, error) {
	var kept []*zip.File
	for _, f := range zr.File {
		n := f.Name
		// What a Mac adds to a zip it makes, and folders, which come with their files.
		if strings.HasPrefix(n, "__MACOSX/") || path.Base(n) == ".DS_Store" || strings.HasSuffix(n, "/") {
			continue
		}
		if strings.Contains(n, `\`) || strings.Contains(n, ":") || !filepath.IsLocal(n) || path.Clean(n) != n {
			return nil, "", badUpload("it holds a file at %q, outside the plugin's folder", n)
		}
		if !f.Mode().IsRegular() {
			return nil, "", badUpload("%s isn't a plain file", n)
		}
		kept = append(kept, f)
	}
	if len(kept) > maxFiles {
		return nil, "", badUpload("it holds %d files; a plugin may have %d", len(kept), maxFiles)
	}
	// One folder holding everything, plugin.toml with it: the folder is the plugin.
	prefix := ""
	if top, _, ok := strings.Cut(firstName(kept), "/"); ok && !hasFile(kept, manifestFile) {
		all := true
		for _, f := range kept {
			all = all && strings.HasPrefix(f.Name, top+"/")
		}
		if all {
			prefix, name = top+"/", top
		}
	}
	id := strings.TrimSuffix(path.Base(filepath.ToSlash(name)), ".zip")
	if !idRe.MatchString(id) {
		return nil, "", badUpload("its name %q can't name a plugin: use letters, digits, '.', '_' and '-', starting with a letter or digit", id)
	}
	files := map[string]*zip.File{}
	var total uint64
	for _, f := range kept {
		rel := strings.TrimPrefix(f.Name, prefix)
		if _, dup := files[rel]; dup {
			return nil, "", badUpload("it holds %s twice", rel)
		}
		if f.UncompressedSize64 > maxSource {
			return nil, "", badUpload("%s is over 1 MB", rel)
		}
		if total += f.UncompressedSize64; total > maxUnpacked {
			return nil, "", badUpload("it unpacks to over %d MB", maxUnpacked>>20)
		}
		files[rel] = f
	}
	for _, need := range []string{manifestFile, mainFile} {
		if files[need] == nil {
			return nil, "", badUpload("it has no %s, at its root or in the one folder there", need)
		}
	}
	return files, id, nil
}

func firstName(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	return files[0].Name
}

func hasFile(files []*zip.File, name string) bool {
	return slices.ContainsFunc(files, func(f *zip.File) bool { return f.Name == name })
}

// unpack writes files into dir, a new folder, and nowhere else.
func unpack(dir string, files map[string]*zip.File) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for rel, f := range files {
		if d := path.Dir(rel); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return badUpload("%s can't be unpacked: %v", rel, err)
			}
		}
		if err := unpackFile(root, rel, f); err != nil {
			return err
		}
	}
	return nil
}

func unpackFile(root *os.Root, rel string, f *zip.File) error {
	r, err := f.Open()
	if err != nil {
		return badUpload("%s can't be read: %v", rel, err)
	}
	defer r.Close()
	out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return badUpload("%s can't be unpacked: %v", rel, err)
	}
	// The zip's own sizes can lie: never more than it said.
	n, err := io.Copy(out, io.LimitReader(r, int64(f.UncompressedSize64)+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return badUpload("%s can't be unpacked: %v", rel, err)
	case uint64(n) > f.UncompressedSize64:
		return badUpload("%s is bigger than the zip says", rel)
	}
	return nil
}
