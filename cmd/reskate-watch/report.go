package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
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

	srcDir         string
	old, cur       *upstream // the source at From and To, when -src is given
	fromRef, toRef string    // the commits From and To were built from: their tags, or later ones
	rebuilt        bool      // To's files were uploaded again after it was checked, and From is To as checked then (fromRef)

	serverTag string          // the release whose server was run
	written   map[string]bool // the settings its config holds: where (dotted) and the manager's keys for them
}

type detail struct{ Title, Body string }

// findings are a report's lists, which another run adds to its own: the
// workflow runs the Windows server on Windows, and the rest on Linux.
type findings struct {
	Breaking, Adds, Notes, Checked []string
}

func (r *report) found() findings { return findings{r.Breaking, r.Adds, r.Notes, r.Checked} }

// include adds the findings another run wrote to path, leaving out what this
// run found too (the config of either server, say).
func (r *report) include(path string) {
	var f findings
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		r.Notes = append(r.Notes, "What the run on another system found couldn't be read: "+code(err.Error()))
		return
	}
	// Of two that say the same, one can say more: the Windows run has no
	// source, so a new setting it finds comes without the README's words.
	add := func(to *[]string, items []string) {
		for _, it := range items {
			i := slices.IndexFunc(*to, func(have string) bool { return strings.HasPrefix(have, it) || strings.HasPrefix(it, have) })
			switch {
			case i < 0:
				*to = append(*to, it)
			case len(it) > len((*to)[i]):
				(*to)[i] = it
			}
		}
	}
	add(&r.Breaking, f.Breaking)
	add(&r.Adds, f.Adds)
	add(&r.Notes, f.Notes)
	add(&r.Checked, f.Checked)
}

// detailCap keeps each background section, and so the issue, under GitHub's
// 65536-character limit for an issue body.
const detailCap = 12000

func (r *report) needsWork() bool { return len(r.Breaking)+len(r.Adds)+len(r.Notes) > 0 }

func (r *report) title() string {
	name := "ReSkate " + r.To
	if r.rebuilt {
		name += ", rebuilt"
	}
	if !r.needsWork() {
		return name + ": nothing to apply"
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
	return name + ": " + strings.Join(parts, ", ")
}

// markdown is the issue body. Its first line marks the release reported, for
// the workflow to find the last one, and the next what its source was
// compared as, for a check of its files uploaded again (-was). Text from
// upstream (commit subjects, strings, the README) only goes in code, where it
// can't mention anyone or break the layout.
func (r *report) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- reskate-watch %s -->\n", r.To)
	if r.toRef != "" {
		fmt.Fprintf(&b, "<!-- reskate-watch-built %s -->\n", r.toRef)
	}
	gh := "https://github.com/" + r.Repo
	fmt.Fprintf(&b, "[ReSkate %s](%s/releases/tag/%s)", r.To, gh, r.To)
	if r.toRef != "" && r.toRef != r.To {
		fmt.Fprintf(&b, ", its files built from %s after the tag", short(r.toRef))
	}
	from, to := r.From, r.To
	if r.fromRef != "" {
		from, to = r.fromRef, r.toRef
	}
	switch {
	case r.rebuilt:
		fmt.Fprintf(&b, " and uploaded again since it was checked, compared with %s as checked then ([changes](%s/compare/%s...%s))", short(from), gh, from, to)
	case r.From != "":
		fmt.Fprintf(&b, ", compared with %s ([changes](%s/compare/%s...%s))", sourceName(r.From, r.fromRef), gh, from, to)
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
