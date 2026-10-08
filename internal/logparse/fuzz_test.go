package logparse

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

var fuzzSeeds = []string{
	"[22:01:51] Thrasher joined (76561198000000001, admin), 3/16 players, loaded in 12 s\n[22:01:52] Thrasher left (Timed out)",
	"[22:01:53] 2 players\n  76561198000000001  Thrasher  (admin)\n  76561198000000002  Some  Guy\r",
	"[22:01:54] My | Server | San Vansterdam | 3/16 players | 60 TPS | voice on (300 m) | password off | code ABCD",
	"[22:01:55] 3 maps\n  San Vansterdam  (now)\n\n  Skate Park",
	"[22:01:56] [chat] Server: hi\n[22:01:57] [party chat] A: \n[22:01:58] Bob is an admin.",
	"a cut-off line\n[1:02:03] not a stamp\r\r\n[22:01:59]\n\n[22:02:00]x",
}

// FuzzAssembler feeds arbitrary server output through the assembler and the
// reply parsers. Nothing may panic, and every line must come out in an
// entry, in order, with only its stamp removed.
func FuzzAssembler(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, out string) {
		lines := strings.Split(out, "\n")
		a := NewAssembler()
		var entries []*Entry
		for _, l := range lines {
			if e := a.Push(l); e != nil {
				entries = append(entries, e)
			}
		}
		if e := a.Flush(); e != nil {
			entries = append(entries, e)
		}
		if a.Pending() {
			t.Fatal("an entry is still pending after Flush")
		}

		var want, got []string
		for _, l := range lines {
			l = strings.TrimRight(l, "\r")
			if _, text, ok := SplitStamp(l); ok {
				l = text
			}
			want = append(want, l)
		}
		for _, e := range entries {
			if e.Kind == "" {
				t.Errorf("%q was not classified", e.Text)
			}
			got = append(got, strings.Split(e.Text, "\n")...)
			// Command replies go through these.
			ParsePlayers(e.Text)
			ParseMaps(e.Text)
			ParseAdminChange(e.Text)
			ParseNetwork(e.Text)
			e.IsEvent()
		}
		if !slices.Equal(got, want) {
			t.Fatalf("lines in:\n%q\nlines out:\n%q", want, got)
		}
	})
}

// FuzzReadLog reads arbitrary log files. Nothing may panic, and keeping the
// last few entries must give the tail of reading them all.
func FuzzReadLog(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(strings.ReplaceAll(s, "[22:", "[2026-10-04 22:"))
	}
	f.Add("[2026-13-45 99:99:99] a date that isn't\n[2026-10-04 22:02:01] Shutting down.")
	f.Fuzz(func(t *testing.T, log string) {
		all := ReadLog(strings.NewReader(log), 1<<20)
		for _, e := range all {
			if e.Kind == "" {
				t.Errorf("%q was not classified", e.Text)
			}
		}
		const keep = 3
		last := ReadLog(strings.NewReader(log), keep)
		if want := all[max(0, len(all)-keep):]; !reflect.DeepEqual(last, want) {
			t.Fatalf("kept %+v, want %+v", last, want)
		}
	})
}
