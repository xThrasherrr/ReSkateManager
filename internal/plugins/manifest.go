package plugins

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// API is the plugin API this manager runs. 0 is the preview, which may still
// change; plugin.toml says which one a plugin was made for.
const API = 0

// Manifest is a plugin's plugin.toml.
type Manifest struct {
	Name        string   `toml:"name" json:"name"`
	Version     string   `toml:"version" json:"version"`
	API         *int     `toml:"api" json:"api"`
	Description string   `toml:"description" json:"description,omitempty"`
	Servers     []string `toml:"servers" json:"servers,omitempty"` // IDs; none means all of them
	Permissions []string `toml:"permissions" json:"permissions,omitempty"`
	Commands    []string `toml:"commands" json:"commands,omitempty"` // the words players type as /word
}

// A plugin's folder name is its ID, in the panel, the audit log and the
// database.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

const (
	manifestFile = "plugin.toml"
	mainFile     = "main.lua"
	maxSource    = 1 << 20 // the most one of a plugin's .lua files may hold
)

// readManifest reads a plugin's plugin.toml, and what in it does nothing.
func readManifest(dir string) (Manifest, []string, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil, errors.New("It has no " + manifestFile + ".")
	}
	if err != nil {
		return m, nil, fmt.Errorf("Its %s can't be read: %w", manifestFile, err)
	}
	md, err := toml.Decode(string(b), &m)
	if err != nil {
		return m, nil, fmt.Errorf("Its %s has a mistake: %w", manifestFile, err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		unknown = append(unknown, k.String())
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		m.Name = filepath.Base(dir)
	}
	switch {
	case m.API == nil:
		return m, unknown, fmt.Errorf("Its %s doesn't say which plugin API it was made for: add api = %d.", manifestFile, API)
	case *m.API != API:
		return m, unknown, fmt.Errorf("It was made for plugin API %d; this manager runs API %d.", *m.API, API)
	}
	if slices.Contains(m.Servers, "*") {
		m.Servers = nil
	}
	for _, w := range m.Commands {
		if !serverconfig.ValidCommandName(w) {
			return m, unknown, fmt.Errorf("Its %s asks for the chat command /%s, which can't be one: a command is 1 to 16 lowercase letters, digits, - or _, and not one of the server's own.", manifestFile, w)
		}
	}
	slices.Sort(m.Commands)
	m.Commands = slices.Compact(m.Commands)
	return m, unknown, nil
}

// moduleFile is the file require(name) loads: "lib.util" is lib/util.lua.
var moduleRe = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`)

func moduleFile(name string) (string, error) {
	if !moduleRe.MatchString(name) || len(name) > 200 {
		return "", fmt.Errorf("bad module name %q: use letters, digits and _, with dots between folders", name)
	}
	return strings.ReplaceAll(name, ".", "/") + ".lua", nil
}

// readSource reads one of a plugin's files, from its folder and nowhere else.
func readSource(dir, file string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.Open(file)
	if err != nil {
		return "", fmt.Errorf("no file %s in the plugin's folder", file)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s isn't a file", file)
	}
	if st.Size() > maxSource {
		return "", fmt.Errorf("%s is over 1 MB", file)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSource+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxSource {
		return "", fmt.Errorf("%s is over 1 MB", file)
	}
	return string(b), nil
}
