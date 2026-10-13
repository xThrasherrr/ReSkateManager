// Package modprofile reads and writes mod profiles as ReSkate's launcher
// (from 2.0.8) and r2modman share them: export.r2x, a short YAML list of
// mods, in a zip (.r2z), or a code on Thunderstore's profile service that
// names that zip. The rules follow Launcher/mod_profiles.cpp.
package modprofile

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Mod is one of a profile's mods: its folder, which for a Thunderstore
// package is its Namespace-Name, the version, and whether it's on.
type Mod struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
}

// Profile is a named list of mods, highest priority first.
type Profile struct {
	Name string
	Mods []Mod
}

// The launcher's limits.
const (
	MaxMods      = 2000
	MaxNameBytes = 48
	// MaxModName is the longest mod folder name (maximum_mod_name); the
	// launcher names a Thunderstore package's folder its full name, cut to it.
	MaxModName = 64
	// MaxPayload is the most a code's payload may be.
	MaxPayload = 8 << 20
	maxExport  = 1 << 20
)

const (
	exportName    = "export.r2x"
	payloadPrefix = "#r2modman"
)

var errTooBig = errors.New("the profile is too large to read")

// ValidName is valid_profile_name: 1 to 48 bytes of printable text with
// nothing a file name refuses (a profile exports as <name>.r2z), not starting
// with a space or ending with a space or a dot.
func ValidName(s string) bool {
	if s == "" || len(s) > MaxNameBytes || s[0] == ' ' || s[len(s)-1] == ' ' || s[len(s)-1] == '.' {
		return false
	}
	return !strings.ContainsFunc(s, refused)
}

// CleanName is clean_profile_name: s with the refused characters dropped,
// cut to length on a character boundary and trimmed, or "Profile" when
// nothing is left.
func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if refused(r) {
			return -1
		}
		return r
	}, s)
	for len(s) > MaxNameBytes {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	s = strings.TrimRight(strings.TrimLeft(s, " "), " .")
	if s == "" {
		return "Profile"
	}
	return s
}

func refused(r rune) bool { return r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) }

// ValidModName is valid_mod_name: a folder name the launcher holds a mod in,
// 1 to 64 letters, digits, _ - . and spaces, not starting with a dot.
func ValidModName(s string) bool {
	if s == "" || len(s) > MaxModName || s[0] == '.' {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-. ", r))
	})
}

// ValidCode is valid_code: a code as the profile service gives them, 8 to 64
// hex digits and dashes.
func ValidCode(s string) bool {
	return len(s) >= 8 && len(s) <= 64 && !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r == '-')
	})
}

// semantic reports whether v is Major.Minor.Patch.
func semantic(v string) bool {
	parts := strings.Split(v, ".")
	return len(parts) == 3 && !slices.ContainsFunc(parts, func(p string) bool {
		return p == "" || strings.Trim(p, "0123456789") != ""
	})
}

// Export is the profile's export.r2x, as the launcher writes it.
func (p Profile) Export() string {
	var b strings.Builder
	b.WriteString("profileName: " + quoted(p.Name) + "\nmods:")
	if len(p.Mods) == 0 {
		b.WriteString(" []\n")
		return b.String()
	}
	b.WriteString("\n")
	for _, m := range p.Mods {
		var v [3]int
		if semantic(m.Version) {
			for i, part := range strings.Split(m.Version, ".") {
				v[i] = number(part)
			}
		}
		fmt.Fprintf(&b, "  - name: %s\n    version:\n      major: %d\n      minor: %d\n      patch: %d\n    enabled: %t\n",
			quoted(m.Name), v[0], v[1], v[2], m.Enabled)
	}
	return b.String()
}

// quoted is s as a double-quoted YAML scalar.
func quoted(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// number is the digits s starts with, as from_chars reads them; 0 for none.
func number(s string) int {
	n := 0
	for _, c := range []byte(s) {
		if c < '0' || c > '9' || n > 1e8 {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func trimmed(s string) string { return strings.TrimRight(strings.TrimLeft(s, " \t"), " \t\r") }

// scalar is a YAML scalar as plain text: quotes taken off and a double-quoted
// one's escapes undone, or an unquoted one up to a comment.
func scalar(s string) string {
	s = trimmed(s)
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var b strings.Builder
		inner := s[1 : len(s)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				i++
			}
			b.WriteByte(inner[i])
		}
		return b.String()
	}
	if at := strings.Index(s, " #"); at >= 0 {
		s = trimmed(s[:at])
	}
	return s
}

// ParseExport reads an export.r2x as parse_export does: line by line, as
// both the launcher and r2modman write it. Mods whose names couldn't be a
// mod folder are left out, and the name is made a valid one.
func ParseExport(text string) (Profile, error) {
	if len(text) > maxExport {
		return Profile{}, errTooBig
	}
	var p Profile
	named, listed := false, false
	var version [3]int
	finish := func() {
		if n := len(p.Mods); n > 0 && p.Mods[n-1].Version == "" {
			p.Mods[n-1].Version = fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2])
		}
	}
	for line := range strings.SplitSeq(text, "\n") {
		top := line != "" && line[0] != ' ' && line[0] != '\t' && line[0] != '-'
		line = trimmed(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '-' {
			// The next mod; what follows the dash is its first field.
			finish()
			if len(p.Mods) >= MaxMods {
				return Profile{}, errors.New("the profile lists more mods than can be read")
			}
			p.Mods = append(p.Mods, Mod{Enabled: true})
			version = [3]int{}
			if line = trimmed(line[1:]); line == "" {
				continue
			}
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = trimmed(key), scalar(value)
		switch {
		case top && key == "profileName":
			p.Name, named = value, true
		case top && key == "mods":
			listed = true
		case len(p.Mods) > 0:
			m := &p.Mods[len(p.Mods)-1]
			switch key {
			case "name":
				m.Name = value
			case "major":
				version[0] = number(value)
			case "minor":
				version[1] = number(value)
			case "patch":
				version[2] = number(value)
			case "enabled":
				m.Enabled = value != "false"
			}
		}
	}
	finish()
	if !named || !listed {
		return Profile{}, errors.New("that is not a mod profile (it has no profileName and mods)")
	}
	p.Name = CleanName(p.Name)
	p.Mods = slices.DeleteFunc(p.Mods, func(m Mod) bool { return !ValidModName(m.Name) })
	return p, nil
}

// Archive is the profile as a .r2z: a zip holding its export.r2x.
func (p Profile) Archive() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// A fixed time, so the same profile makes the same file.
	w, err := zw.CreateHeader(&zip.FileHeader{Name: exportName, Method: zip.Store, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, p.Export()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseArchive reads a .r2z, or a bare export.r2x, which r2modman also takes
// where a .r2z is asked for. The config files in an r2modman one are ignored.
func ParseArchive(b []byte) (Profile, error) {
	if !bytes.HasPrefix(b, []byte("PK")) {
		return ParseExport(string(b))
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return Profile{}, errors.New("the profile file is not a readable archive")
	}
	for _, f := range zr.File {
		if !strings.EqualFold(f.Name, exportName) || f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > maxExport {
			return Profile{}, errTooBig
		}
		rc, err := f.Open()
		if err != nil {
			return Profile{}, errors.New("the profile file is damaged")
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxExport+1))
		rc.Close()
		if err != nil {
			return Profile{}, errors.New("the profile file is damaged")
		}
		return ParseExport(string(data))
	}
	return Profile{}, errors.New("the profile file has no export.r2x in it")
}

// Payload is what the profile service keeps for a code: "#r2modman", a line
// break and the .r2z in base64.
func (p Profile) Payload() ([]byte, error) {
	a, err := p.Archive()
	if err != nil {
		return nil, err
	}
	return []byte(payloadPrefix + "\n" + base64.StdEncoding.EncodeToString(a)), nil
}

// ParsePayload reads what a code names. Like the launcher, it takes the
// base64 with or without padding and line breaks.
func ParsePayload(b []byte) (Profile, error) {
	if len(b) > MaxPayload {
		return Profile{}, errTooBig
	}
	rest, ok := bytes.CutPrefix(b, []byte(payloadPrefix))
	if !ok {
		return Profile{}, errors.New("that code does not lead to a mod profile")
	}
	text := strings.Map(func(r rune) rune {
		if r == '=' || r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, string(rest))
	data, err := base64.RawStdEncoding.DecodeString(text)
	if err != nil {
		return Profile{}, errors.New("the profile code holds text that is not a profile")
	}
	return ParseArchive(data)
}
