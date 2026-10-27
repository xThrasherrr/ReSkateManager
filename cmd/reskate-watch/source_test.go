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
	var got []string
	for _, l := range cppLiterals(src) {
		got = append(got, l.text)
	}
	if !slices.Equal(got, want) {
		t.Errorf("cppLiterals:\n got %q\nwant %q", got, want)
	}
}

// 2.0.6's window tells lines apart by tags in them that no line has: text
// only compared with isn't what the server writes.
func TestCppLiteralsCompared(t *testing.T) {
	src := `if (tag == "[ban]" || tag == "[banned]") return red;
if (text.find("[cmd]") != std::string::npos || text.starts_with("[error]")) level = error;
if ("[left]" == tag) return red;
log_("[parks] " + name);
if (name == "kick") kick();
const auto *x = "[cmd]";`
	got := map[string]bool{}
	for _, l := range cppLiterals(src) {
		got[l.text] = got[l.text] || !l.compared
	}
	want := map[string]bool{"[ban]": false, "[banned]": false, "[cmd]": true, "[error]": false, "[left]": false, "[parks] ": true, "kick": false}
	if !maps.Equal(got, want) {
		t.Errorf("written:\n got %v\nwant %v", got, want)
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

// A run spanning two literals, " needed" and ")." at 2.0.6: 2.0.7 wrote
// " needed of " instead, and votePassedRe's " needed)." stopped matching.
func TestEndMoved(t *testing.T) {
	src := func(lits ...string) *upstream {
		u := &upstream{strings: map[string]bool{}, all: strings.Join(lits, "\x00")}
		for _, l := range lits {
			u.strings[l] = true
		}
		return u
	}
	r := &report{old: src(" yes, ", " no, ", " needed", ")."), cur: src(" yes, ", " no, ", " needed of ", " vote", ").")}
	if start, longer := r.endMoved(" needed)."); start != " needed" || !slices.Equal(longer, []string{" needed of "}) {
		t.Errorf("endMoved = %q, %q", start, longer)
	}
	// A run that ends where a literal did is followed by a group, which may
	// take what comes now: votePassedRe's " needed(?: of ...)?".
	for _, run := range []string{" needed", " yes, ", ")."} {
		if start, _ := r.endMoved(run); start != "" {
			t.Errorf("endMoved(%q) = %q", run, start)
		}
	}
	r.checkLost(&usage{frags: map[string][]string{" needed).": {"internal/instance/instance.go:1"}}, runs: map[string]bool{" needed).": true}})
	if len(r.Breaking) != 1 || !strings.Contains(r.Breaking[0], `" needed of "`) {
		t.Errorf("breaking %q", r.Breaking)
	}
}

// The names the server refuses for custom votes, chat commands and role tags,
// by the function that checks them, not its declaration nor one before it.
func TestReservedNames(t *testing.T) {
	if got := reservedNames("bool custom_vote_name_free(std::string_view name) noexcept;\nstruct X { int a{}; };"); len(got) != 0 {
		t.Errorf("a declaration gave %q", got)
	}
	src := `bool custom_vote_name_free(std::string_view name) noexcept {
    for (const std::string_view taken : {"map", "kick", "extend"})
        if (name == taken) return false;
    return !std::all_of(name.begin(), name.end(), [](char c) { return c >= '0' && c <= '9'; });
}`
	if got := reservedNames(src); !slices.Equal(got["custom_vote_name_free"], []string{"map", "kick", "extend"}) {
		t.Errorf("reservedNames = %q", got)
	}
	// ReSkate's main after 2.0.7, where the list is a loop in a loop.
	src = `static bool valid_role_tag(std::string_view tag) { return tag.size() <= 16; }
std::string roles_error(const Roles &roles) {
    const auto plain = [](std::string_view text) {
        std::string out;
        return out;
    };
    for (std::size_t i = 0; i < roles.list.size(); ++i) {
        // What the game itself calls the server, a lobby's host, friends and the ReSkate team.
        for (const std::string_view taken : {"server", "reskate", "reskateadmin"})
            if (plain(role.tag) == taken) return where + "the tag \"" + role.tag + "\" is one the game uses itself.";
    }
    return {};
}`
	got := reservedNames(src)
	if !slices.Equal(got["roles_error"], []string{"server", "reskate", "reskateadmin"}) || len(got) != 1 {
		t.Errorf("reservedNames = %q", got)
	}
}

// A name the server refuses and the manager allows is a config the server
// won't start with: 2.0.7's extend, for custom votes.
func TestCheckReserved(t *testing.T) {
	r := &report{old: &upstream{}, cur: &upstream{reserved: map[string][]string{
		"custom_vote_name_free": {"map", "extend", "skip"}, "custom_command_name_free": {"help"}, "park_name_free": {"x"},
		"roles_error": {"server", "reskateadmin"}}}}
	r.checkReserved()
	if len(r.Breaking) != 1 || !strings.Contains(r.Breaking[0], `"skip"`) || strings.Contains(r.Breaking[0], `"extend"`) {
		t.Errorf("breaking %q, want skip alone", r.Breaking)
	}
	if len(r.Adds) != 1 || !strings.Contains(r.Adds[0], "park_name_free") {
		t.Errorf("adds %q, want the new name check", r.Adds)
	}
}

// A bare verb the help listed gains forms, which matter when the panel takes
// the verb for a read-only one: 2.0.7's maps reload. One the help never
// listed is known by its verb alone.
func TestNewForms(t *testing.T) {
	r := &report{From: "v2.0.6", To: "v2.0.7",
		old: &upstream{cmds: map[string][]string{"maps": {"maps"}, "status": {"status"}, "frob": {"frob"}}, unlisted: map[string]bool{"frob": true}, lines: map[string]bool{}},
		cur: &upstream{cmds: map[string][]string{"maps": {"maps [reload]"}, "status": {"status [verbose]"}, "frob": {"frob <n>"}}, lines: map[string]bool{}}}
	r.srcDir = t.TempDir()
	r.checkSource(&usage{frags: map[string][]string{}, words: map[string]bool{}})
	want := []string{"New form of command `\"maps\"`: `\"maps [reload]\"`",
		"New form of command `\"status\"`: `\"status [verbose]\"`, which the panel's console lets anyone who may run commands use, as it does `\"status\"`: decide its permission (internal/auth/console.go)"}
	if !slices.Equal(r.Adds, want) {
		t.Errorf("adds\n got %q\nwant %q", r.Adds, want)
	}
	// The manager sending it is taking it up.
	r.Adds = nil
	r.checkSource(&usage{frags: map[string][]string{}, words: map[string]bool{"maps reload": true, "status verbose": true}})
	if len(r.Adds) != 0 {
		t.Errorf("adds %q, want none", r.Adds)
	}
}

func TestCommands(t *testing.T) {
	got := commands("status | net [player] | say <text>\nlisted on|off | votes [map|kick]\npoll <question> | <answer> | <answer>... | poll end\n" +
		"score-allow [<fingerprint>|remove <fingerprint>] | words [reload | global on|off]\nreserved [slots <n> | add|remove <SteamID64>] | roles [reload]")
	want := map[string][]string{"status": {"status"}, "net": {"net [player]"}, "say": {"say <text>"}, "listed": {"listed on|off"}, "votes": {"votes [map|kick]"},
		"poll": {"poll <question> | <answer> | <answer>...", "poll end"}, "score-allow": {"score-allow [<fingerprint>|remove <fingerprint>]"},
		"words": {"words [reload | global on|off]"}, "reserved": {"reserved [slots <n> | add|remove <SteamID64>]"}, "roles": {"roles [reload]"}}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

// 2.0.3's announce-to puts its player inside the tag's bracket, which only
// the log parser itself can say it reads.
func TestTagLines(t *testing.T) {
	src := `log_("[announcement" + (target ? " -> " + guest_name(*target) : std::string()) + "] " + line);
log_("[vote] " + label);
write_log("[afk" + "] b");
log_("[card by plugin " + std::string(plugin) + " -> " + who + "] " + line);`
	got := tagLines(src)
	if want := map[string]string{"[announcement": "[announcement -> x] x", "[afk": "[afk] bx", "[card by plugin ": "[card by plugin x -> x] x"}; !maps.Equal(got, want) {
		t.Errorf("tagLines = %q, want %q", got, want)
	}
	if !parsed(got["[announcement"]) || !parsed(got["[card by plugin "]) || parsed("[announcements -> x] x") || parsed("") {
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

// A server plugin's lines are written by Host::log (2.1.2); the manager's
// plugins hear the server's through them, so a new shape of them is breaking.
func TestCheckPluginLog(t *testing.T) {
	src := `void Host::console(std::string_view text) { environment_.console(text); }
void Host::log(std::string_view plugin, LogLevel level, std::string_view text) {
    environment_.log(level, std::format("[{}] {}", plugin, text));
}
std::string Host::level() { return "x"; }`
	if got := pluginLog(src); got != "[{}] {}" {
		t.Fatalf("pluginLog = %q", got)
	}
	logs := &upstream{strings: map[string]bool{"[Plugins] ": true}, pluginLog: "[{}] {}"}
	for _, c := range []struct {
		old, cur *upstream
		breaking bool
	}{
		{&upstream{}, logs, false},
		{logs, logs, false},
		{logs, &upstream{strings: logs.strings, pluginLog: "[plugin {}] {}"}, true},
		{logs, &upstream{strings: map[string]bool{"[plugins] ": true}, pluginLog: "[{}] {}"}, true},
		{logs, &upstream{}, true},
		{&upstream{}, &upstream{}, false}, // before 2.1.2
	} {
		r := &report{old: c.old, cur: c.cur}
		r.checkPluginLog()
		if len(r.Breaking) > 0 != c.breaking {
			t.Errorf("%q to %q: breaking %q", c.old.pluginLog, c.cur.pluginLog, r.Breaking)
		}
	}
}
