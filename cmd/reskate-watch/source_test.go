package main

import (
	"maps"
	"slices"
	"testing"
)

func TestCppStrings(t *testing.T) {
	src := `#include "server_host.h"
// a "comment"
/* a "block" comment */
const char *joined = "one" /* between */ "two";
auto wide = L"wide";
char quote = '"';
int n = 1'000'000;
auto r = R"x(raw "text")x";
log("tab\there\n" + std::to_string(id) + " joined (");
std::string s = u8"\x41\101\"";
`
	want := []string{"server_host.h", "onetwo", "wide", `raw "text"`, "tab\there\n", " joined (", `AA"`}
	if got := cppStrings(src); !slices.Equal(got, want) {
		t.Errorf("cppStrings:\n got %q\nwant %q", got, want)
	}
}

func TestLogStarts(t *testing.T) {
	src := `write_log("Mods: " + std::to_string(n));
log_(on ? "[steam] Signed in." : "[steam] Signed out: " + why);
log_(std::string("Could not start; start ") + exe + " again.");
log_(guest_name(g) + " joined (" + id + ")");
helper("not logged");`
	want := []string{"Mods: ", "[steam] Signed in.", "[steam] Signed out: ", "Could not start; start "}
	if got := logStarts(src); !slices.Equal(got, want) {
		t.Errorf("logStarts:\n got %q\nwant %q", got, want)
	}
}

func TestCommands(t *testing.T) {
	got := commands("status | net [player] | say <text>\nlisted on|off | votes [map|kick]")
	want := map[string][]string{"status": {"status"}, "net": {"net [player]"}, "say": {"say <text>"}, "listed": {"listed on|off"}, "votes": {"votes [map|kick]"}}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

func TestTags(t *testing.T) {
	got := tags(map[string]bool{"[party chat] ": true, "[afk] ": true, "[DM from ": true, "plain": true})
	if want := map[string]bool{"party chat": true, "afk": true}; !maps.Equal(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
}

func TestMeaningful(t *testing.T) {
	for s, want := range map[string]bool{" joined (": true, "), ": false, "on": false, "1234": false, "Join code: ": true} {
		if got := meaningful(s); got != want {
			t.Errorf("meaningful(%q) = %v", s, got)
		}
	}
}
