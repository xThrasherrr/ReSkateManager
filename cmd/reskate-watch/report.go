package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// report is what one run found. Breaking and Adds are the work for the
// manager, Notes what could not be checked, Details the background a person
// reads to do the work.
type report struct {
	Repo, From, To string

	Breaking []string // the manager relies on something the release changed
	Adds     []string // new in the release, for the manager to take up
	Notes    []string
	Checked  []string // what was looked at, so a clean report shows it looked
	Details  []detail

	srcDir   string
	old, cur *upstream // the source at From and To, when -src is given
}

type detail struct{ Title, Body string }

// detailCap keeps each background section, and so the issue, under GitHub's
// 65536-character limit for an issue body.
const detailCap = 12000

func (r *report) needsWork() bool { return len(r.Breaking)+len(r.Adds)+len(r.Notes) > 0 }

func (r *report) title() string {
	if !r.needsWork() {
		return "ReSkate " + r.To + ": nothing to apply"
	}
	var parts []string
	for _, c := range []struct {
		n    int
		what string
	}{{len(r.Breaking), "breaking"}, {len(r.Adds), "new"}, {len(r.Notes), "unchecked"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	return "ReSkate " + r.To + ": " + strings.Join(parts, ", ")
}

// markdown is the issue body. Its first line marks the release reported, for
// the workflow to find the last one. Text from upstream (commit subjects,
// strings, the README) only goes in code, where it can't mention anyone or
// break the layout.
func (r *report) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- reskate-watch %s -->\n", r.To)
	gh := "https://github.com/" + r.Repo
	fmt.Fprintf(&b, "[ReSkate %s](%s/releases/tag/%s)", r.To, gh, r.To)
	if r.From != "" {
		fmt.Fprintf(&b, ", compared with %s ([changes](%s/compare/%s...%s))", r.From, gh, r.From, r.To)
	}
	b.WriteString(".\n")
	if !r.needsWork() {
		b.WriteString("\nNothing for the manager to change.\n")
	}
	list := func(title string, items []string, box bool) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n### %s\n\n", title)
		for _, it := range items {
			if box {
				b.WriteString("- [ ] " + it + "\n")
			} else {
				b.WriteString("- " + it + "\n")
			}
		}
	}
	list("Breaking: the manager relies on these", r.Breaking, true)
	list("New: for the manager to take up", r.Adds, true)
	list("Not checked", r.Notes, false)
	list("Checked", r.Checked, false)
	for _, d := range r.Details {
		body := d.Body
		if len(body) > detailCap {
			cut := strings.LastIndexByte(body[:detailCap], '\n')
			body = body[:cut+1] + "... (cut; see the changes link above)\n"
		}
		fmt.Fprintf(&b, "\n<details><summary>%s</summary>\n\n%s\n</details>\n", d.Title, body)
	}
	return b.String()
}

// code shows s as inline code, quoted so spaces at its ends and line breaks
// show.
func code(s string) string { return inline(strconv.Quote(s)) }

// inline shows s as inline code as it is: a JSON value, say.
func inline(s string) string {
	ticks := "`"
	for strings.Contains(s, ticks) {
		ticks += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return ticks + s + ticks
}

// fence shows text as a fenced block, its fence longer than any run of
// backticks inside.
func fence(lang, text string) string {
	ticks := "```"
	for strings.Contains(text, ticks) {
		ticks += "`"
	}
	return ticks + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + ticks
}

// managerLink points at a line of the manager: on GitHub when run there,
// else as path:line.
func managerLink(where string) string {
	sha, repo := os.Getenv("GITHUB_SHA"), os.Getenv("GITHUB_REPOSITORY")
	if sha == "" || repo == "" {
		return "`" + where + "`"
	}
	file, line, _ := strings.Cut(where, ":")
	return fmt.Sprintf("[%s](https://github.com/%s/blob/%s/%s#L%s)", where, repo, sha, file, line)
}
