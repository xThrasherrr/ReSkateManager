package main

import (
	"fmt"
	"maps"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
)

// upstream is the dedicated server's source at one release: every string
// literal in Server/ (its tests aside), the console's commands and the
// README.
type upstream struct {
	strings map[string]bool
	all     string              // every literal, NUL between them, for substring searches
	help    string              // the `help` reply, which lists the commands
	cmds    map[string][]string // each command's forms in the help, by verb; just the verb when it has none
	lines   map[string]bool     // how the lines the server logs start: the first literal it logs
	readme  string
}

func (r *report) loadSource(src string) error {
	r.srcDir = src
	var err error
	if r.old, err = readUpstream(src, r.From); err != nil {
		return err
	}
	r.cur, err = readUpstream(src, r.To)
	return err
}

func git(src string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", src}, args...)...).Output()
	if ee, ok := err.(*exec.ExitError); ok {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
	}
	return string(out), err
}

func readUpstream(src, tag string) (*upstream, error) {
	list, err := git(src, "ls-tree", "-r", "--name-only", tag, "--", "Server/")
	if err != nil {
		return nil, err
	}
	u := &upstream{strings: map[string]bool{}, lines: map[string]bool{}}
	verbs := map[string]bool{}
	for name := range strings.FieldsSeq(list) {
		if strings.HasPrefix(name, "Server/Test/") || !slices.Contains([]string{".cpp", ".h", ".hpp", ".cc", ".inl"}, path.Ext(name)) {
			continue
		}
		text, err := git(src, "show", tag+":"+name)
		if err != nil {
			return nil, err
		}
		for _, s := range cppStrings(text) {
			u.strings[s] = true
			if strings.Count(s, " | ") > strings.Count(u.help, " | ") {
				u.help = s
			}
		}
		for _, m := range verbRe.FindAllStringSubmatch(text, -1) {
			verbs[m[1]] = true
		}
		for _, s := range logStarts(text) {
			u.lines[s] = true
		}
	}
	if strings.Count(u.help, " | ") < 10 {
		u.help = ""
	}
	u.cmds = commands(u.help)
	for v := range verbs {
		if u.cmds[v] == nil {
			u.cmds[v] = []string{v}
		}
	}
	u.all = strings.Join(slices.Sorted(maps.Keys(u.strings)), "\x00")
	u.readme, _ = git(src, "show", tag+":Server/README.txt")
	return u, nil
}

// checkSource compares the source at From and To: text the manager uses that
// the server no longer has, and new commands, console tags, lines and flags.
func (r *report) checkSource(use *usage) {
	for _, frag := range slices.Sorted(maps.Keys(use.frags)) {
		if strings.Contains(r.old.all, frag) && !strings.Contains(r.cur.all, frag) {
			where := use.frags[frag]
			links := make([]string, 0, 3)
			for _, w := range where[:min(len(where), 3)] {
				links = append(links, managerLink(w))
			}
			if len(where) > 3 {
				links = append(links, fmt.Sprintf("%d more", len(where)-3))
			}
			r.Breaking = append(r.Breaking, fmt.Sprintf("The server's source no longer has %s, which the manager uses at %s", code(frag), strings.Join(links, ", ")))
		}
	}

	r.Checked = append(r.Checked, fmt.Sprintf("%d strings in the manager's code, against the server's source at %s and %s.", len(use.frags), r.From, r.To))
	oldCmds, newCmds := r.old.cmds, r.cur.cmds
	if len(newCmds) == 0 {
		r.Notes = append(r.Notes, "The server's commands weren't found in its source, so new ones weren't looked for.")
	}
	for _, verb := range slices.Sorted(maps.Keys(newCmds)) {
		forms, was := newCmds[verb], oldCmds[verb]
		if was == nil {
			if !use.sends(verb) {
				r.Adds = append(r.Adds, "New command "+codes(forms))
			}
			continue
		}
		var added []string
		for _, f := range forms {
			// A command the help didn't list before is known only by its verb.
			if f != verb && !slices.Contains(was, f) && !slices.Contains(was, verb) {
				added = append(added, f)
			}
		}
		if len(added) > 0 {
			r.Adds = append(r.Adds, fmt.Sprintf("New form of command %s: %s", code(verb), codes(added)))
		}
	}
	var gone []string
	for _, verb := range slices.Sorted(maps.Keys(oldCmds)) {
		if newCmds[verb] == nil && len(newCmds) > 0 {
			gone = append(gone, oldCmds[verb]...)
		}
	}
	if len(gone) > 0 {
		// Those the manager sends also show as text it uses, above.
		r.Details = append(r.Details, detail{fmt.Sprintf("Commands gone from the help (%d)", len(gone)), fence("text", strings.Join(gone, "\n"))})
	}

	// Every tag the parser lacks, not only new ones: one it never had
	// files those lines as plain text.
	for _, t := range slices.Sorted(maps.Keys(tags(r.cur.strings))) {
		if !use.knowsTag(t) {
			r.Adds = append(r.Adds, fmt.Sprintf("Console tag %s, which the log parser doesn't know", code("["+t+"]")))
		}
	}
	var heads []string
	lines := map[string][]string{} // by their "Word: ", when they have one
	for _, s := range slices.Sorted(maps.Keys(r.cur.lines)) {
		if r.old.lines[s] || tagRe.MatchString(s) || !meaningful(s) || use.starts(s) {
			continue
		}
		head := s
		if i := strings.Index(s, ": "); i > 0 && i < 30 {
			head = s[:i+2]
		}
		if lines[head] == nil {
			heads = append(heads, head)
		}
		lines[head] = append(lines[head], s)
	}
	for _, head := range heads {
		if l := lines[head]; len(l) == 1 {
			r.Adds = append(r.Adds, fmt.Sprintf("New console line %s, which the log parser reads as plain text", code(l[0])))
		} else {
			r.Adds = append(r.Adds, fmt.Sprintf("%d new console lines starting %s, which the log parser reads as plain text: %s", len(l), code(head), codes(l)))
		}
	}
	for _, s := range slices.Sorted(maps.Keys(r.cur.strings)) {
		if flagRe.MatchString(s) && !r.old.strings[s] {
			r.Adds = append(r.Adds, "New command-line flag "+code(s))
		}
	}

	r.sourceDetails()
}

var (
	flagRe = regexp.MustCompile(`^--[a-z][a-z0-9-]+$`)
	tagRe  = regexp.MustCompile(`^\[([a-z][a-z ]*[a-z])\]`)
	// The verbs Host::command tests for, which the help doesn't always list
	// (chat-color in 2.0.0).
	verbRe = regexp.MustCompile(`\b(?:name|verb) == "([a-z][a-z0-9]*(?:-[a-z0-9]+)*)"`)
	// A call that logs a line: write_log in main.cpp, log_ in the host.
	logRe = regexp.MustCompile(`\b(?:write_log|log_)\(`)
)

// commands reads the help reply ("status | net [player] | ...") into each
// command's forms by its verb.
func commands(help string) map[string][]string {
	out := map[string][]string{}
	for line := range strings.SplitSeq(help, "\n") {
		for entry := range strings.SplitSeq(line, " | ") {
			entry = strings.TrimSpace(entry)
			if verb, _, _ := strings.Cut(entry, " "); verb != "" && !slices.Contains(out[verb], entry) {
				out[verb] = append(out[verb], entry)
			}
		}
	}
	return out
}

// codes shows each of list as code, joined.
func codes(list []string) string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = code(s)
	}
	return strings.Join(out, ", ")
}

// logStarts finds how the lines the server logs start: in each call that logs
// one, the literals that begin the line, so in log_(on ? "A" : "B" + x) both
// "A" and "B", and in log_(std::string("A") + x) "A", but not one added on.
func logStarts(text string) []string {
	var out []string
	for _, m := range logRe.FindAllStringIndex(text, -1) {
		depth, prev := 1, byte('(')
		for i := m[1]; i < len(text) && depth > 0; {
			c := text[i]
			switch {
			case c == '"':
				s, next := quoted(text, i, '"')
				if prev == '(' || prev == '?' || prev == ':' && text[i-1] != ':' {
					out = append(out, s)
				}
				i, prev = next, '"'
				continue
			case c == '(':
				depth++
			case c == ')':
				depth--
			case c == ';':
				depth = 0
			}
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				prev = c
			}
			i++
		}
	}
	return out
}

// tags finds the "[tag] " the server starts console lines with.
func tags(lits map[string]bool) map[string]bool {
	out := map[string]bool{}
	for s := range lits {
		if m := tagRe.FindStringSubmatch(s); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// sourceDetails adds the background: the server's commits, its README's
// changes, and the strings that came and went.
func (r *report) sourceDetails() {
	if log, err := git(r.srcDir, "log", "--no-merges", "--format=%h %s", r.From+".."+r.To, "--", "Server/"); err == nil && log != "" {
		n := strings.Count(log, "\n")
		r.Details = append(r.Details, detail{fmt.Sprintf("Server commits (%d)", n), fence("text", log)})
	}
	if diff, err := git(r.srcDir, "diff", "--no-color", "-U1", r.From, r.To, "--", "Server/README.txt"); err == nil && diff != "" {
		r.Details = append(r.Details, detail{"Server/README.txt changes", fence("diff", diff)})
	}
	changed := func(a, b *upstream) []string {
		var out []string
		for s := range a.strings {
			if !b.strings[s] && s != a.help && meaningful(s) {
				out = append(out, s)
			}
		}
		slices.Sort(out)
		return out
	}
	for _, c := range []struct {
		title string
		list  []string
	}{{"New strings in the server's source", changed(r.cur, r.old)}, {"Strings gone from the server's source", changed(r.old, r.cur)}} {
		if len(c.list) == 0 {
			continue
		}
		lines := make([]string, len(c.list))
		for i, s := range c.list {
			lines[i] = strings.ReplaceAll(s, "\n", `\n`)
		}
		r.Details = append(r.Details, detail{fmt.Sprintf("%s (%d)", c.title, len(c.list)), fence("text", strings.Join(lines, "\n"))})
	}
}

// meaningful is whether a string says enough to be worth matching on: four
// characters or more, three of them letters.
func meaningful(s string) bool {
	if len(strings.TrimSpace(s)) < 4 {
		return false
	}
	letters := 0
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			letters++
		}
	}
	return letters >= 3
}

// cppStrings returns the string literals in C++ source with their escapes
// resolved, adjacent ones joined as the compiler joins them. It skips
// comments, character literals and numbers (1'000), so a quote in any of them
// can't throw it.
func cppStrings(src string) []string {
	var out []string
	joinable := false // only spaces and comments since the last literal
	add := func(s string) {
		if joinable {
			out[len(out)-1] += s
		} else {
			out = append(out, s)
		}
		joinable = true
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case strings.HasPrefix(src[i:], "//"):
			i += strings.IndexByte(src[i:]+"\n", '\n')
		case strings.HasPrefix(src[i:], "/*"):
			if end := strings.Index(src[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(src)
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"':
			s, next := quoted(src, i, '"')
			add(s)
			i = next
		case c == '\'':
			_, i = quoted(src, i, '\'')
			joinable = false
		case isIdent(c) && !isDigit(c):
			j := i
			for j < len(src) && isIdent(src[j]) {
				j++
			}
			word := src[i:j]
			switch {
			case j < len(src) && src[j] == '"' && slices.Contains([]string{"L", "u", "U", "u8"}, word):
				s, next := quoted(src, j, '"')
				add(s)
				i = next
			case j < len(src) && src[j] == '"' && slices.Contains([]string{"R", "LR", "uR", "UR", "u8R"}, word):
				s, next := raw(src, j)
				add(s)
				i = next
			default:
				i = j
				joinable = false
			}
		case isDigit(c):
			for i++; i < len(src); i++ {
				d := src[i]
				if isIdent(d) || d == '.' || d == '\'' && i+1 < len(src) && isIdent(src[i+1]) ||
					(d == '+' || d == '-') && strings.IndexByte("eEpP", src[i-1]) >= 0 {
					continue
				}
				break
			}
			joinable = false
		default:
			i++
			joinable = false
		}
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isIdent(c byte) bool {
	return isDigit(c) || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// quoted reads the literal opening at src[at] up to its closing quote q.
func quoted(src string, at int, q byte) (string, int) {
	var b strings.Builder
	i := at + 1
	for i < len(src) {
		c := src[i]
		switch {
		case c == q:
			return b.String(), i + 1
		case c == '\n': // unterminated: give up at the line's end
			return b.String(), i
		case c == '\\' && i+1 < len(src):
			i++
			switch e := src[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\n': // a line continued
			case 'x':
				v, n := 0, 0
				for ; i+1 < len(src) && n < 2 && strings.IndexByte("0123456789abcdefABCDEF", src[i+1]) >= 0; n++ {
					i++
					v = v*16 + strings.IndexByte("0123456789abcdef", src[i]|0x20)
				}
				b.WriteByte(byte(v))
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for n := 1; n < 3 && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '7'; n++ {
						i++
						v = v*8 + int(src[i]-'0')
					}
					if v != 0 { // \0 ends a C string; it is in no text
						b.WriteByte(byte(v))
					}
				} else {
					b.WriteByte(e) // \" \\ \' \?
				}
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i
}

// raw reads R"delim(...)delim" from its quote at src[at].
func raw(src string, at int) (string, int) {
	open := strings.IndexByte(src[at:], '(')
	if open < 0 {
		return "", len(src)
	}
	end := ")" + src[at+1:at+open] + `"`
	body := at + open + 1
	n := strings.Index(src[body:], end)
	if n < 0 {
		return src[body:], len(src)
	}
	return src[body : body+n], body + n + len(end)
}
