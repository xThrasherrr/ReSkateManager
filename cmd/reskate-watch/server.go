package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/updater"
)

// checkServer installs the latest release for both platforms as a manager
// does, then runs this machine's server twice: with no config, to see the
// settings it writes, and on that config once the manager has edited it, to
// see the server still starts on what the manager writes.
func (r *report) checkServer(ctx context.Context) {
	tmp, err := os.MkdirTemp("", "reskate-watch-")
	if err != nil {
		r.Notes = append(r.Notes, "The server checks had no folder to work in: "+code(err.Error()))
		return
	}
	defer os.RemoveAll(tmp)
	dir := ""
	var installed []string
	for _, goos := range []string{"windows", "linux"} {
		d, tag, err := install(ctx, r.Repo, goos, tmp)
		if err != nil {
			r.Breaking = append(r.Breaking, fmt.Sprintf("The manager can't install the latest release's %s server: %s", platform[goos], code(err.Error())))
			continue
		}
		installed = append(installed, fmt.Sprintf("%s (%s)", platform[goos], tag))
		if r.To == "" {
			r.To = tag
		}
		if goos == runtime.GOOS {
			dir, r.serverTag = d, tag
		}
	}
	if len(installed) > 0 {
		r.Checked = append(r.Checked, "The latest release installs with the manager's updater: "+strings.Join(installed, ", ")+".")
	}
	if dir == "" {
		if !updater.Supported() {
			r.Notes = append(r.Notes, "The server only runs on Windows and Linux, so its config wasn't checked.")
		}
		return
	}
	r.checkConfig(ctx, dir)
}

var platform = map[string]string{"windows": "Windows", "linux": "Linux"}

// install downloads and installs the latest release's server for goos into
// a folder of tmp with the manager's updater, and returns the folder and the
// release's tag.
func install(ctx context.Context, repo, goos, tmp string) (string, string, error) {
	c := updater.NewChecker(repo)
	c.GOOS = goos
	var rel *updater.Release
	var archive string
	// GitHub or the download can fail for a moment; a report saying the
	// manager can't install a release should mean it.
	var err error
	for try := range 3 {
		if try > 0 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(20 * time.Second):
			}
		}
		if rel, err = c.Latest(ctx, true); err == nil {
			if archive, err = c.Download(ctx, rel, filepath.Join(tmp, "cache"), nil); err == nil {
				break
			}
		}
	}
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(tmp, goos)
	if _, err := updater.Install(archive, dir); err != nil {
		return "", rel.Tag, err
	}
	exe := "ReSkateServer"
	if goos == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(filepath.Join(dir, exe)); err != nil {
		return "", rel.Tag, fmt.Errorf("the install has no %s", exe)
	}
	return dir, rel.Tag, nil
}

// checkConfig runs the server installed in dir. Steam's library is taken
// away first, so the server stops at loading it, after its config and before
// it could sign in to Steam.
func (r *report) checkConfig(ctx context.Context, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		r.Notes = append(r.Notes, "The install couldn't be read: "+code(err.Error()))
		return
	}
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name()), "steam_api") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	cfg := filepath.Join(dir, "ReSkateServer.json")
	os.Remove(cfg) // a release may ship one; the server's own default is wanted
	os.Remove(filepath.Join(dir, serverconfig.BansFile))

	lines, err := runServer(ctx, dir)
	if err != nil {
		r.Notes = append(r.Notes, "The server couldn't be run: "+code(err.Error()))
		return
	}
	if _, err := os.Stat(cfg); err != nil {
		r.Notes = append(r.Notes, "The server wrote no config. It printed: "+code(strings.Join(lines, "\n")))
		return
	}
	f, err := serverconfig.Read(cfg)
	if err != nil {
		r.Breaking = append(r.Breaking, "The manager can't read the config the server writes: "+code(err.Error()))
		return
	}
	if n, of := r.compareConfig(f); n == 0 {
		r.Checked = append(r.Checked, fmt.Sprintf("The %s server's own config has %d settings, all known to the manager.", platform[runtime.GOOS], of))
	} else {
		r.Checked = append(r.Checked, fmt.Sprintf("The %s server's own config has %d settings, %d of them new to the manager.", platform[runtime.GOOS], of, n))
	}

	// The manager's edit: every setting set to the manager's default, an
	// admin and a ban, written the way the settings, admins and bans pages do.
	for _, fd := range serverconfig.Fields {
		if !slices.Contains(serverconfig.Removed, fd.Key) {
			f.Set(fd.Key, fd.Default)
		}
	}
	f.SetAdmins([]string{"76561198000000001"})
	f.SetBans([]serverconfig.Ban{{ID: "76561198000000002", Name: "reskate-watch", Added: time.Now().Unix()}})
	if err := f.Write(cfg); err != nil {
		r.Notes = append(r.Notes, "The edited config couldn't be written: "+code(err.Error()))
		return
	}
	if lines, err = runServer(ctx, dir); err != nil {
		r.Notes = append(r.Notes, "The server couldn't be run on the edited config: "+code(err.Error()))
		return
	}
	reached := false
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "Config problem: "), strings.HasPrefix(l, "Cannot read "):
			r.Breaking = append(r.Breaking, "The server won't start on a config the manager edited: "+code(l))
			reached = true
		case strings.HasPrefix(l, "Added new settings to "):
			r.Breaking = append(r.Breaking, "The server misses settings in a config the manager edited: "+code(l))
		case strings.Contains(l, "steam_api"), strings.HasPrefix(l, "Signing in to Steam"):
			reached = true
		}
	}
	if !reached {
		r.Notes = append(r.Notes, "The server stopped before its Steam step on the edited config. It printed: "+code(strings.Join(lines, "\n")))
	} else {
		r.Checked = append(r.Checked, "The server reads a config the manager edited: every setting, an admin and a ban.")
	}
}

// compareConfig checks the config a fresh server wrote against the settings
// the manager knows, and returns how many are new to it, of how many.
func (r *report) compareConfig(f *serverconfig.File) (int, int) {
	known := serverconfig.Paths()
	placeOf := map[string]string{}
	for place, key := range known {
		placeOf[key] = place
	}
	var leaves []string
	leafPaths(f.Root, "", &leaves)
	slices.Sort(leaves)
	r.written = map[string]bool{}
	for _, p := range leaves {
		r.written[p] = true
		if key, ok := known[p]; ok {
			r.written[key] = true
		}
	}
	added := 0
	for _, p := range leaves {
		if _, ok := known[p]; !ok {
			added++
			v, _ := at(f.Root, p)
			r.Adds = append(r.Adds, fmt.Sprintf("New setting %s, default %s%s", code(p), inline(jsonText(v)), r.describe(p)))
		}
	}
	for _, p := range slices.Sorted(maps.Keys(known)) {
		if _, ok := at(f.Root, p); !ok {
			what := known[p]
			if fd, ok := serverconfig.FieldByKey(what); ok {
				what = fd.Label
			}
			r.Breaking = append(r.Breaking, fmt.Sprintf("The server no longer writes %s, which the manager keeps as %s", code(p), what))
		}
	}
	vals := f.Values()
	for _, fd := range serverconfig.Fields {
		if slices.Contains(serverconfig.Removed, fd.Key) {
			continue
		}
		raw, ok := f.Get(fd.Key)
		if !ok {
			continue // reported above
		}
		v, ok := vals[fd.Key]
		switch {
		case !ok:
			r.Breaking = append(r.Breaking, fmt.Sprintf("The server's default for %s, %s, fails the manager's check for %s", code(placeOf[fd.Key]), inline(jsonText(raw)), fd.Label))
		case fmt.Sprint(v) != fmt.Sprint(fd.Default):
			r.Adds = append(r.Adds, fmt.Sprintf("%s (%s) now defaults to %s; the manager has %s", fd.Label, code(placeOf[fd.Key]), inline(jsonText(v)), inline(jsonText(fd.Default))))
		}
	}
	return added, len(leaves)
}

// describe finds a setting in the server's README.txt, which lists each as
// its name, then what it does on that line and the indented ones after.
func (r *report) describe(place string) string {
	if r.cur == nil {
		return ""
	}
	name := place[strings.LastIndexByte(place, '.')+1:]
	lines := strings.Split(r.cur.readme, "\n")
	for i, l := range lines {
		rest, ok := strings.CutPrefix(l, name+" ")
		if !ok {
			continue
		}
		text := strings.Fields(rest)
		for _, more := range lines[i+1:] {
			if !strings.HasPrefix(more, "    ") {
				break
			}
			text = append(text, strings.Fields(more)...)
		}
		s := strings.Join(text, " ")
		if len(s) > 400 {
			s = s[:400] + "..."
		}
		return ": " + code(s)
	}
	return ""
}

// runServer runs the server in dir until it stops, or until it reaches the
// step after its config (Steam), where it is stopped.
func runServer(ctx context.Context, dir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, updater.ExeName()), "--no-update")
	cmd.Dir = dir
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		_, text, _ := logparse.SplitStamp(strings.TrimRight(sc.Text(), "\r"))
		lines = append(lines, text)
		// The Windows server waits on a failure until someone reads it, so
		// it is stopped as soon as the outcome is known.
		if strings.HasPrefix(text, "Signing in to Steam") || strings.HasPrefix(text, "Config problem: ") ||
			strings.HasPrefix(text, "Cannot read ") || strings.Contains(text, "steam_api") {
			cmd.Process.Kill()
			break
		}
	}
	cmd.Wait() // the lines say how it went, not the exit code
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return lines, errors.New("the server ran for a minute without reaching Steam")
	}
	return lines, nil
}

// leafPaths lists the dotted paths of the values in a config tree. An empty
// object (the world layers, say) counts as a value.
func leafPaths(v any, prefix string, out *[]string) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 && prefix != "" {
		*out = append(*out, prefix)
		return
	}
	for k, child := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		leafPaths(child, p, out)
	}
}

// at finds the value at a dotted path.
func at(root map[string]any, path string) (any, bool) {
	var cur any = root
	for part := range strings.SplitSeq(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func jsonText(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}
