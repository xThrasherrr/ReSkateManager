package main

import (
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

func TestRegexRuns(t *testing.T) {
	for re, want := range map[string][]string{
		`^(.+) joined \((\d+)(, admin)?\), (\d+)/(\d+) players(?:, loaded in (\d+) s)?$`: {" joined (", ", admin", "), ", "/", " players", ", loaded in ", " s"},
		`^\[(chat|command|party chat)\] (.*)$`:                                           {"[", "chat", "command", "party chat", "] "},
		`^Steam ID (\d+)(?: \([^)]*\))?, public IP (.*)\.$`:                              {"Steam ID ", " (", ")", ", public IP ", "."},
		`^(7656119\d{10}) is (an admin|no longer an admin)\.$`:                           {"7656119", " is ", "an admin", "no longer an admin", "."},
		`^colou?r$`: {"colo", "r"},
	} {
		if got := regexRuns(re); !slices.Equal(got, want) {
			t.Errorf("regexRuns(%q)\n got %q\nwant %q", re, got, want)
		}
	}
}

func TestSendsForm(t *testing.T) {
	u := &usage{words: map[string]bool{"votes polls ": true, "votes ": true}}
	for form, want := range map[string]bool{
		"votes polls off|admins|everyone": true,
		"votes poll-seconds <n>":          false,
		"votes [<vote> on|off|<percent>]": false, // only "votes" is fixed, which every form has
	} {
		if got := u.sendsForm(form); got != want {
			t.Errorf("sendsForm(%q) = %v", form, got)
		}
	}
}

func TestAddFile(t *testing.T) {
	src := "package x\n\nvar re = regexp.MustCompile(`^Join code: (\\S+)$`)\n\nfunc f() string { return fmt.Sprintf(\"voice-range %d\", 3) }\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	u := &usage{frags: map[string][]string{}, words: map[string]bool{}, runs: map[string]bool{}, parse: map[string]bool{}}
	u.addFile(fset, f, "internal/x/x.go")
	if got := u.frags["Join code: "]; !slices.Equal(got, []string{"internal/x/x.go:3"}) {
		t.Errorf("Join code: at %v", got)
	}
	if got := u.frags["voice-range "]; !slices.Equal(got, []string{"internal/x/x.go:5"}) {
		t.Errorf("voice-range at %v", got)
	}
	if len(u.frags) != 2 {
		t.Errorf("frags = %v, want those two", u.frags)
	}
	if !u.runs["Join code: "] || u.runs["voice-range "] {
		t.Errorf("runs = %v, want only the pattern's", u.runs)
	}
}
