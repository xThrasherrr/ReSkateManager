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
	got := commands("status | net [player] | say <text>\nlisted on|off | votes [map|kick]\npoll <question> | <answer> | <answer>... | poll end")
	want := map[string][]string{"status": {"status"}, "net": {"net [player]"}, "say": {"say <text>"}, "listed": {"listed on|off"}, "votes": {"votes [map|kick]"},
		"poll": {"poll <question> | <answer> | <answer>...", "poll end"}}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

// 2.0.3's announce-to puts its player inside the tag's bracket, which only
// the log parser itself can say it reads.
func TestTagLines(t *testing.T) {
	src := `log_("[announcement" + (target ? " -> " + guest_name(*target) : std::string()) + "] " + line);
log_("[vote] " + label);
write_log("[afk" + "] b");`
	got := tagLines(src)
	if want := map[string]string{"[announcement": "[announcement -> x] x", "[afk": "[afk] bx"}; !maps.Equal(got, want) {
		t.Errorf("tagLines = %q, want %q", got, want)
	}
	if !parsed(got["[announcement"]) || parsed("[announcements -> x] x") || parsed("") {
		t.Error("parsed doesn't follow the log parser")
	}
}

// Where the host logs what a command it ran itself answers: a passed vote's
// since 1.1.x, a chat command's and a poll-run's since 2.0.3.
func TestSelfRuns(t *testing.T) {
	src := `void Host::run_custom_command(Guest &guest, const CustomCommand &c, std::string_view argument) {
    for (const auto &run : runs)
        log_(command(fill(fill(run, "{player}", std::to_string(id)),
                          "{map}", config_.map)));
}
std::string Host::start_poll(Guest *starter) {
    log_("[poll] command(" + x + ")");
}
void Host::check_vote(bool expired) {
    if (top) log_(command(fill(done.run, "{answer})", leaders.front())));
    switch (vote.kind) {
    case VoteKind::map: log_( command("map " + done.value)); break;
    }
}`
	want := []string{
		`Host::run_custom_command: command(fill(fill(run, "{player}", std::to_string(id)), "{map}", config_.map))`,
		`Host::check_vote: command(fill(done.run, "{answer})", leaders.front()))`,
		`Host::check_vote: command("map " + done.value)`,
	}
	if got := selfRuns(src); !slices.Equal(got, want) {
		t.Errorf("selfRuns:\n got %q\nwant %q", got, want)
	}
}

// A new command counts as taken up once the panel's console decides who may
// run it, whatever words the manager happens to have: 2.0.3's vote and poll
// are also log tags.
func TestNewCommands(t *testing.T) {
	r := &report{From: "v2.0.2", To: "v2.0.3",
		old: &upstream{cmds: map[string][]string{"status": {"status"}}, lines: map[string]bool{}},
		cur: &upstream{cmds: map[string][]string{"status": {"status"}, "vote": {"vote <map|kick>"}, "frobnicate": {"frobnicate <n>"}},
			lines: map[string]bool{}}}
	r.srcDir = t.TempDir() // no git: the background details are skipped
	r.checkSource(&usage{frags: map[string][]string{}, words: map[string]bool{"vote": true, "frobnicate": true}})
	if want := []string{"New command `\"frobnicate <n>\"`"}; !slices.Equal(r.Adds, want) {
		t.Errorf("adds %q, want %q", r.Adds, want)
	}
}

func TestNarrowed(t *testing.T) {
	was := []string{"announcements [list|add <text>|remove <n>|clear|interval <minutes>|off|card on|off]"}
	if !narrowed("announcements [list|add <text>|remove <n>|clear|interval <minutes>|off]", was) {
		t.Error("a form that only dropped card on|off is new")
	}
	if narrowed("announcements [list|add <text>|pin <n>]", was) {
		t.Error("a form with pin isn't new")
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
