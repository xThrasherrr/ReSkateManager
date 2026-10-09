package main

import (
	"maps"
	"slices"
	"strings"
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
log_("[network] Behind: " + std::to_string(n) + (first ? "" : "; if this keeps coming") + ".");
log_((on ? "Up" : "Down") + std::string(", now ") + std::string("not a start"));
helper("not logged");`
	want := []string{"Mods: ", "[steam] Signed in.", "[steam] Signed out: ", "Could not start; start ", "[network] Behind: ", "Up", "Down"}
	if got := logStarts(src); !slices.Equal(got, want) {
		t.Errorf("logStarts:\n got %q\nwant %q", got, want)
	}
}

func TestHostCommand(t *testing.T) {
	src := `// Host::command(...) runs the console's commands; this comment names it.
void Host::chat_command(Guest &g, std::string_view line) {
    if (verb == "poll") return;
}
std::string Host::command(std::string_view line, std::uint64_t admin) {
    const std::string name = verb == "voice-allow" ? "voice" : verb; // }
    if (name == "votes") { return "}}"; }
    const auto n = 1'000; /* } */
    if (name == "announce") return '}' == 'x' ? "a" : "b";
}
void Host::other() { if (name == "outside") {} }
`
	var got []string
	for _, m := range verbRe.FindAllStringSubmatch(hostCommand(src), -1) {
		got = append(got, m[1])
	}
	if want := []string{"voice-allow", "votes", "announce"}; !slices.Equal(got, want) {
		t.Errorf("verbs %q, want %q", got, want)
	}
	if hostCommand("void f() {}") != "" {
		t.Error("found Host::command where there is none")
	}
}

// Text the source no longer spells out isn't lost while the server still
// writes it as a setting, which only a run of that release's server shows.
func TestCheckLost(t *testing.T) {
	src := func(lits ...string) *upstream { return &upstream{all: strings.Join(lits, "\x00")} }
	use := &usage{frags: map[string][]string{"votes.seconds": {"internal/serverconfig/schema.go:1"}, "Join code: ": {"internal/logparse/parse.go:1"}}}
	r := &report{From: "v2.0.1", To: "v2.0.2", old: src("votes.seconds", "Join code: "), cur: src("votes.", "seconds"),
		serverTag: "v2.0.2", written: map[string]bool{"votes.seconds": true}}
	r.checkLost(use)
	if len(r.Breaking) != 1 || !strings.Contains(r.Breaking[0], `"Join code: "`) {
		t.Errorf("breaking %q, want Join code only", r.Breaking)
	}
	r.Breaking, r.serverTag = nil, "v2.0.3" // the server run was a newer release
	r.checkLost(use)
	if len(r.Breaking) != 2 {
		t.Errorf("breaking %q, want both", r.Breaking)
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
