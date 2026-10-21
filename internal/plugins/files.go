package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// File is one of a plugin's files, as its editor lists them.
type File struct {
	Path string `json:"path"` // from the plugin's folder, with slashes
	Size int64  `json:"size"`
}

// ErrBadFile is a file the editor can't read or write.
var ErrBadFile = errors.New("can't edit that file")

// ErrChanged is a save over a file that changed since the editor read it.
var ErrChanged = errors.New("it changed since it was opened")

func badFile(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadFile, fmt.Sprintf(format, a...))
}

// Files is what's in a plugin's folder, leaving out what starts with a dot.
func (m *Manager) Files(id string) ([]File, error) {
	root, err := m.openPlugin(id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []File
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular():
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, File{Path: p, Size: info.Size()})
		if len(out) == maxFiles {
			return fs.SkipAll
		}
		return nil
	})
	return out, err
}

// ReadFile is one of a plugin's files as text, and the hash a save of it
// gives back.
func (m *Manager) ReadFile(id, rel string) (text, hash string, err error) {
	if err := checkPath(rel); err != nil {
		return "", "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.pluginLocked(id)
	if err != nil {
		return "", "", err
	}
	text, err = readSource(p.dir, rel)
	if err != nil {
		return "", "", badFile("%v", err)
	}
	if !utf8.ValidString(text) {
		return "", "", badFile("%s isn't text", rel)
	}
	return text, sum(text), nil
}

// WriteFile replaces one of a plugin's files with text, or adds it. was is
// the hash ReadFile gave, "" for a new file; when the file on disk no longer
// has it, nothing is written, and ErrChanged comes with the hash it has.
// The plugin keeps running its old code until it's reloaded.
func (m *Manager) WriteFile(id, rel, text, was string) (hash string, err error) {
	if err := checkPath(rel); err != nil {
		return "", err
	}
	if len(text) > maxSource {
		return "", badFile("%s would be over 1 MB", rel)
	}
	if !utf8.ValidString(text) {
		return "", badFile("%s isn't text", rel)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.pluginLocked(id)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	cur := ""
	switch st, err := root.Lstat(rel); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	case !st.Mode().IsRegular():
		return "", badFile("%s isn't a file", rel)
	default:
		old, err := readSource(p.dir, rel)
		if err != nil {
			return "", badFile("%v", err)
		}
		cur = sum(old)
	}
	if cur != was {
		return cur, ErrChanged
	}
	// Whole or not at all: a running plugin may require it mid-save.
	if d := path.Dir(rel); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil {
			return "", badFile("%s can't be written: %v", rel, err)
		}
	}
	tmp := path.Join(path.Dir(rel), "."+path.Base(rel)+"-"+randomName()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer root.Remove(tmp) // gone already, once renamed
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, rel)
	}
	if err != nil {
		return "", err
	}
	m.note(p, "info", "Saved "+rel+" from the editor.")
	return sum(text), nil
}

func (m *Manager) openPlugin(id string) (*os.Root, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.pluginLocked(id)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(p.dir)
}

func (m *Manager) pluginLocked(id string) (*Plugin, error) {
	m.scanLocked()
	p, ok := m.plugins[id]
	if !ok {
		return nil, ErrNotFound
	}
	return p, nil
}

// checkPath takes a path in a plugin's folder, with slashes, and nothing
// hidden: no part of it starts with a dot.
func checkPath(rel string) error {
	if rel == "" || len(rel) > 255 || strings.ContainsAny(rel, `\:`) || !filepath.IsLocal(rel) || path.Clean(rel) != rel {
		return badFile("%q isn't a path in the plugin's folder", rel)
	}
	for part := range strings.SplitSeq(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return badFile("%q is hidden: no part of a path may start with a dot", rel)
		}
	}
	return nil
}

func sum(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:8])
}
