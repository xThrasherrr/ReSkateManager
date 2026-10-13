package modprofile

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

var street = Profile{Name: "Street maps", Mods: []Mod{
	{Name: "Sk8r-BBCity", Version: "1.2.3", Enabled: true},
	{Name: "Drip-Deck", Version: "2.0.0", Enabled: false},
}}

// What the launcher writes, byte for byte, and reads back.
func TestExport(t *testing.T) {
	want := "profileName: \"Street maps\"\nmods:\n" +
		"  - name: \"Sk8r-BBCity\"\n    version:\n      major: 1\n      minor: 2\n      patch: 3\n    enabled: true\n" +
		"  - name: \"Drip-Deck\"\n    version:\n      major: 2\n      minor: 0\n      patch: 0\n    enabled: false\n"
	if got := street.Export(); got != want {
		t.Fatalf("export\n%s\nwant\n%s", got, want)
	}
	if got := (Profile{Name: "Empty"}).Export(); got != "profileName: \"Empty\"\nmods: []\n" {
		t.Errorf("empty export %q", got)
	}
	p, err := ParseExport(street.Export())
	if err != nil || !reflect.DeepEqual(p, street) {
		t.Errorf("read back %+v, %v", p, err)
	}
}

// r2modman's own export.r2x: unquoted, with its own keys and comments, and a
// mod whose name couldn't be a folder, which is left out.
func TestParseR2modman(t *testing.T) {
	text := "# exported\r\nprofileName: 'Tom''s: maps?'\r\n# mods follow\r\nmods:\r\n" +
		"- name: BepInEx-BepInExPack\r\n  version:\r\n    major: 5\r\n    minor: 4\r\n    patch: 2100\r\n  enabled: true\r\n" +
		"- name: bad/name\r\n  version:\r\n    major: 1\r\n    minor: 0\r\n    patch: 0\r\n  enabled: true\r\n" +
		"- enabled: false\r\n  name: \"Sk8r-\\\"BB\"\r\n"
	p, err := ParseExport(text)
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{Name: "Tom's maps", Mods: []Mod{{Name: "BepInEx-BepInExPack", Version: "5.4.2100", Enabled: true}}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("read %+v\nwant %+v", p, want)
	}
	for _, bad := range []string{"", "mods: []\n", "profileName: x\n", strings.Repeat("x", maxExport+1)} {
		if _, err := ParseExport(bad); err == nil {
			t.Errorf("%.20q read as a profile", bad)
		}
	}
}

func TestArchiveAndPayload(t *testing.T) {
	a, err := street.Archive()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := street.Archive(); !bytes.Equal(a, again) {
		t.Error("the same profile made two different files")
	}
	if p, err := ParseArchive(a); err != nil || !reflect.DeepEqual(p, street) {
		t.Errorf("archive read back %+v, %v", p, err)
	}
	// A bare export.r2x, and an r2modman .r2z with its config files.
	if p, err := ParseArchive([]byte(street.Export())); err != nil || p.Name != "Street maps" {
		t.Errorf("bare export read %+v, %v", p, err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"config/BepInEx.cfg": "[x]", "EXPORT.R2X": street.Export()} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	if p, err := ParseArchive(buf.Bytes()); err != nil || len(p.Mods) != 2 {
		t.Errorf("r2modman archive read %+v, %v", p, err)
	}
	if _, err := ParseArchive([]byte("PK\x03\x04 not a zip")); err == nil {
		t.Error("a broken zip was read")
	}

	pay, err := street.Payload()
	if err != nil || !bytes.HasPrefix(pay, []byte("#r2modman\n")) {
		t.Fatalf("payload %.40q, %v", pay, err)
	}
	if p, err := ParsePayload(pay); err != nil || !reflect.DeepEqual(p, street) {
		t.Errorf("payload read back %+v, %v", p, err)
	}
	// Without padding, and broken over lines, as the launcher also reads it.
	loose := "#r2modman\r\n" + strings.TrimRight(base64.StdEncoding.EncodeToString(a), "=")
	loose = loose[:30] + "\n" + loose[30:]
	if p, err := ParsePayload([]byte(loose)); err != nil || p.Name != "Street maps" {
		t.Errorf("loose payload read %+v, %v", p, err)
	}
	for _, bad := range []string{"#r2modman\n!!", "r2modman\n" + base64.StdEncoding.EncodeToString(a), "<html>"} {
		if _, err := ParsePayload([]byte(bad)); err == nil {
			t.Errorf("%.20q read as a payload", bad)
		}
	}
}

func TestNamesAndCodes(t *testing.T) {
	for in, want := range map[string]string{
		"Street maps":                       "Street maps",
		` <My> "server": maps? `:            "My server maps",
		"...":                               "Profile",
		"":                                  "Profile",
		strings.Repeat("é", 30):             strings.Repeat("é", 24),
		"[US] Skate 3 | discord.gg/x":       "[US] Skate 3  discord.ggx",
		"[US] Skate 3 | discord.gg/x/maps.": "[US] Skate 3  discord.ggxmaps",
	} {
		got := CleanName(in)
		if got != want || !ValidName(got) {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", " x", "x ", "x.", "a/b", strings.Repeat("x", 49)} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q)", bad)
		}
	}
	for s, want := range map[string]bool{"Sk8r-BBCity": true, "Some Mod 1.0": true, ".hidden": false, "a/b": false, "": false, strings.Repeat("x", 65): false} {
		if ValidModName(s) != want {
			t.Errorf("ValidModName(%q) = %v", s, !want)
		}
	}
	for s, want := range map[string]bool{"018f2b1c-7d3e-4a5b-9c8d-0e1f2a3b4c5d": true, "abcdef12": true, "abc": false, "zzzzzzzz": false, "0123 4567": false} {
		if ValidCode(s) != want {
			t.Errorf("ValidCode(%q) = %v", s, !want)
		}
	}
}
