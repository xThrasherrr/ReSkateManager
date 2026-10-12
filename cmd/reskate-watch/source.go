package main

import (
	"fmt"
	"maps"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/api"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// upstream is the dedicated server's source at one release: every string
// literal in Server/ (its tests and window aside), the console's commands
// and the README.
type upstream struct {
	strings  map[string]bool
	written  map[string]bool     // of those, the ones not only compared with: text the server may print
	all      string              // every literal, NUL between them, for substring searches
	help     string              // the `help` reply, which lists the commands
	cmds     map[string][]string // each command's forms in the help, by verb; just the verb when it has none
	lines    map[string]bool     // how the lines the server logs start: the first literal it logs
	tagLines map[string]string   // of those, the ones that add to a tag's bracket, each with a line it writes
	selfRuns map[string]string   // where the server logs the answer to a command it runs itself, by file
	readme   string
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
	u := &upstream{strings: map[string]bool{}, written: map[string]bool{}, lines: map[string]bool{}, tagLines: map[string]string{}, selfRuns: map[string]string{}}
	verbs := map[string]bool{}
	for name := range strings.FieldsSeq(list) {
		// Not the tests, nor the server's window (2.0.6, Windows), which the
		// manager never opens: it starts servers headless.
		if strings.HasPrefix(name, "Server/Test/") || strings.HasPrefix(name, "Server/GUI/") ||
			!slices.Contains([]string{".cpp", ".h", ".hpp", ".cc", ".inl"}, path.Ext(name)) {
			continue
		}
		text, err := git(src, "show", tag+":"+name)
		if err != nil {
			return nil, err
		}
		for _, l := range cppLiterals(text) {
			u.strings[l.text] = true
			if !l.compared {
				u.written[l.text] = true
			}
			if strings.Count(l.text, " | ") > strings.Count(u.help, " | ") {
				u.help = l.text
			}
		}
		for _, m := range verbRe.FindAllStringSubmatch(hostCommand(text), -1) {
			verbs[m[1]] = true
		}
		for _, s := range logStarts(text) {
			u.lines[s] = true
		}
		maps.Copy(u.tagLines, tagLines(text))
		for _, run := range selfRuns(text) {
			u.selfRuns[run] = name
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
	r.checkLost(use)

	r.Checked = append(r.Checked, fmt.Sprintf("%d strings in the manager's code, against the server's source at %s and %s.", len(use.frags), r.From, r.To))
	oldCmds, newCmds := r.old.cmds, r.cur.cmds
	if len(newCmds) == 0 {
		r.Notes = append(r.Notes, "The server's commands weren't found in its source, so new ones weren't looked for.")
	}
	// A new command is taken up once the panel's console has decided who may
	// run it: the watcher is built from the same tree as the manager it reads.
	decided := api.ConsoleVerbs()
	for _, verb := range slices.Sorted(maps.Keys(newCmds)) {
		forms, was := newCmds[verb], oldCmds[verb]
		if was == nil {
			if !slices.Contains(decided, verb) {
				r.Adds = append(r.Adds, "New command "+codes(forms))
			}
			continue
		}
		var added []string
		for _, f := range forms {
			// A command the help didn't list before is known only by its verb.
			if f != verb && !slices.Contains(was, f) && !slices.Contains(was, verb) && !use.sendsForm(f) && !narrowed(f, was) {
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
	// files those lines as plain text. Only those the server may write: 2.0.6's
	// window tells lines apart by tags no line has ("[cmd]", "[fatal]").
	for _, t := range slices.Sorted(maps.Keys(tags(r.cur.written))) {
		if !use.knowsTag(t) {
			r.Adds = append(r.Adds, fmt.Sprintf("Console tag %s, which the log parser doesn't know", code("["+t+"]")))
		}
	}
	var heads []string
	lines := map[string][]string{} // by their "Word: ", when they have one
	for _, s := range slices.Sorted(maps.Keys(r.cur.lines)) {
		if r.old.lines[s] || tagRe.MatchString(s) || !meaningful(s) || use.starts(s) || parsed(r.cur.tagLines[s]) {
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
		// One the manager starts servers with is taken up (2.0.6's --headless).
		if flagRe.MatchString(s) && !r.old.strings[s] && !use.words[s] {
			r.Adds = append(r.Adds, "New command-line flag "+code(s))
		}
	}
	for _, run := range slices.Sorted(maps.Keys(r.cur.selfRuns)) {
		if _, was := r.old.selfRuns[run]; !was {
			fn, call, _ := strings.Cut(run, ": ")
			r.Adds = append(r.Adds, fmt.Sprintf("The server logs the answer to a command it runs itself, in %s (%s): %s, which the manager could take for the reply to one of its own",
				inline(fn), inline(r.cur.selfRuns[run]), inline(call)))
		}
	}

	r.sourceDetails()
}

// checkLost reports the text the manager uses that the source had at From
// and no longer has at To. A setting the server still writes isn't lost, only
// no longer spelled out whole: 2.0.2 built "votes.seconds" from "votes." and
// a key instead.
func (r *report) checkLost(use *usage) {
	for _, frag := range slices.Sorted(maps.Keys(use.frags)) {
		if !strings.Contains(r.old.all, frag) || strings.Contains(r.cur.all, frag) {
			continue
		}
		if r.serverTag == r.To && r.written[frag] {
			continue
		}
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

var (
	flagRe = regexp.MustCompile(`^--[a-z][a-z0-9-]+$`)
	tagRe  = regexp.MustCompile(`^\[([a-z][a-z ]*[a-z])\]`)
	// Where Host::command is defined: not in a comment that names it.
	hostCommandRe = regexp.MustCompile(`(?m)^[^/\n]*\bHost::command\(`)
	// The verbs Host::command tests for, which the help doesn't always list
	// (chat-color in 2.0.0). Only there: players' chat commands test theirs
	// the same way (/poll in 2.0.2).
	verbRe = regexp.MustCompile(`\b(?:name|verb) == "([a-z][a-z0-9]*(?:-[a-z0-9]+)*)"`)
	// A call that logs a line: write_log in main.cpp, log_ in the host.
	logRe = regexp.MustCompile(`\b(?:write_log|log_)\(`)
	// A line that opens a tag's bracket and adds to it: "[announcement" + " -> " + name + "] ".
	openTagRe = regexp.MustCompile(`^\[[a-z][a-z ]*[a-z]$`)
	// The host logging what a command it ran itself answers: a passed vote's,
	// and since 2.0.3 a chat command's and a poll-run's. The console's own
	// replies are main.cpp's write_log(host.command(line)).
	selfRunRe = regexp.MustCompile(`\blog_\(\s*command\(`)
	// A function's definition, which starts its line: "void Host::check_vote(".
	funcRe = regexp.MustCompile(`(?m)^[A-Za-z_][^\n;{}()]*?\b([A-Za-z_]\w*(?:::\w+)*)\(`)
)

// parsed reports whether the manager's log parser makes something of a line
// the server writes, rather than plain text.
func parsed(line string) bool {
	if line == "" {
		return false
	}
	e := logparse.Entry{Text: line}
	logparse.Classify(&e)
	return e.Kind != logparse.KindRaw
}

// tagLines finds the log calls whose line opens a tag's bracket and adds to
// it before closing it, which the textual checks can't judge: each start with
// a line it writes, its next literal filled out with x.
func tagLines(text string) map[string]string {
	out := map[string]string{}
	for _, m := range logRe.FindAllStringIndex(text, -1) {
		var lits []string
		for i := m[1]; i < len(text) && text[i] != ';' && len(lits) < 2; i++ {
			if text[i] == '"' {
				s, next := quoted(text, i, '"')
				lits, i = append(lits, s), next-1
			}
		}
		if len(lits) < 2 || !openTagRe.MatchString(lits[0]) {
			continue
		}
		line := lits[0] + lits[1] + "x"
		if !strings.Contains(lits[1], "]") {
			line += "] x"
		}
		out[lits[0]] = line
	}
	return out
}

// selfRuns finds where the host logs the answer to a command it runs itself,
// each as its function and the command it runs: "Host::check_vote:
// command(done.value)".
func selfRuns(text string) []string {
	defs := funcRe.FindAllStringSubmatchIndex(text, -1)
	var out []string
	for _, m := range selfRunRe.FindAllStringIndex(text, -1) {
		fn := "?"
		for _, d := range defs {
			if d[0] < m[0] {
				fn = text[d[2]:d[3]]
			}
		}
		// The call, to its closing parenthesis; one in a literal doesn't count.
		at := strings.LastIndex(text[:m[1]], "command(")
		depth, i := 0, at
		for i < len(text) {
			c := text[i]
			if c == '"' || c == '\'' {
				_, i = quoted(text, i, c)
				continue
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				if depth--; depth == 0 {
					i++
					break
				}
			}
			i++
		}
		out = append(out, fn+": "+strings.Join(strings.Fields(text[at:i]), " "))
	}
	return out
}

// hostCommand is the body of Host::command, which runs the console's
// commands, in a source file, or "" when the file doesn't define it. Braces
// in literals, comments and numbers (1'000) don't count.
func hostCommand(src string) string {
	m := hostCommandRe.FindStringIndex(src)
	if m == nil {
		return ""
	}
	at := m[0]
	open := strings.IndexByte(src[at:], '{')
	if open < 0 {
		return ""
	}
	start, depth := at+open, 0
	for i := start; i < len(src); {
		c := src[i]
		switch {
		case strings.HasPrefix(src[i:], "//"):
			i += strings.IndexByte(src[i:]+"\n", '\n')
			continue
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return src[start:]
			}
			i += end + 4
			continue
		case c == '"' || c == '\'':
			_, i = quoted(src, i, c)
			continue
		case isDigit(c):
			for i++; i < len(src) && (isIdent(src[i]) || src[i] == '\'' && i+1 < len(src) && isIdent(src[i+1])); i++ {
			}
			continue
		case isIdent(c):
			for i++; i < len(src) && isIdent(src[i]); i++ {
			}
			continue
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return src[start : i+1]
			}
		}
		i++
	}
	return src[start:]
}

// commands reads the help reply ("status | net [player] | ...") into each
// command's forms by its verb. A form with a | of its own ("poll <question> |
// <answer>..." in 2.0.3, "words [reload | global on|off]" in 2.0.6) leaves
// pieces that start with a <placeholder>, not a verb, or close a bracket the
// piece before opened: they belong to the form before them.
func commands(help string) map[string][]string {
	out := map[string][]string{}
	for line := range strings.SplitSeq(help, "\n") {
		var forms []string
		for entry := range strings.SplitSeq(line, " | ") {
			open := len(forms) > 0 && strings.Count(forms[len(forms)-1], "[") > strings.Count(forms[len(forms)-1], "]")
			if entry = strings.TrimSpace(entry); (strings.HasPrefix(entry, "<") || open) && len(forms) > 0 {
				forms[len(forms)-1] += " | " + entry
			} else {
				forms = append(forms, entry)
			}
		}
		for _, form := range forms {
			if verb, _, _ := strings.Cut(form, " "); verb != "" && !slices.Contains(out[verb], form) {
				out[verb] = append(out[verb], form)
			}
		}
	}
	return out
}

// narrowed reports whether a command's form has only words its old forms had,
// so it takes nothing new: 2.0.3's announcements only dropped "card on|off".
func narrowed(form string, was []string) bool {
	words := func(f string) []string {
		return strings.FieldsFunc(f, func(r rune) bool { return strings.ContainsRune(" []<>|", r) })
	}
	old := map[string]bool{}
	for _, f := range was {
		for _, w := range words(f) {
			old[w] = true
		}
	}
	for _, w := range words(form) {
		if !old[w] {
			return false
		}
	}
	return true
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
// "A" and "B", and in log_(std::string("A") + x) "A", but not one added on,
// in parentheses or not: log_("A" + (first ? "" : "B")) is "A" alone.
func logStarts(text string) []string {
	var out []string
	for _, m := range logRe.FindAllStringIndex(text, -1) {
		// added: at each depth of parentheses, whether this branch of it has
		// had a + yet.
		added, prev := []bool{false}, byte('(')
		for i := m[1]; i < len(text) && len(added) > 0; {
			c := text[i]
			switch {
			case c == '"':
				s, next := quoted(text, i, '"')
				if (prev == '(' || prev == '?' || prev == ':') && !slices.Contains(added, true) {
					out = append(out, s)
				}
				i, prev = next, '"'
				continue
			case c == ':' && (text[i-1] == ':' || i+1 < len(text) && text[i+1] == ':'):
				c = 'S' // std::, not a branch
			case c == '?' || c == ':':
				added[len(added)-1] = false
			case c == '+':
				added[len(added)-1] = true
			case c == '(':
				added = append(added, false)
			case c == ')':
				added = added[:len(added)-1]
			case c == ';':
				added = nil
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

// cppLiteral is one literal of cppLiterals, and whether the source only
// compares text with it: tag == "[ban]", text.find("[cmd]"),
// starts_with("[error]").
type cppLiteral struct {
	text     string
	compared bool
}

// comparedBefore and comparedAfter find a comparison next to a literal.
var (
	comparedBefore = regexp.MustCompile(`(?:[=!]=|\b(?:r?find|starts_with|ends_with|contains|compare)\()\s*$`)
	comparedAfter  = regexp.MustCompile(`^\s*[=!]=`)
)

// cppLiterals returns the string literals in C++ source with their escapes
// resolved, adjacent ones joined as the compiler joins them, each with whether
// it's only compared. It skips comments, character literals and numbers
// (1'000), so a quote in any of them can't throw it.
func cppLiterals(src string) []cppLiteral {
	var out []cppLiteral
	joinable := false // only spaces and comments since the last literal
	start := 0        // where the literal being read began
	add := func(s string, end int) {
		if joinable {
			out[len(out)-1].text += s
		} else {
			out = append(out, cppLiteral{text: s})
		}
		last := &out[len(out)-1]
		if !joinable {
			last.compared = comparedBefore.MatchString(src[max(0, start-32):start])
		}
		last.compared = last.compared || comparedAfter.MatchString(src[end:min(end+8, len(src))])
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
			start = i
			s, next := quoted(src, i, '"')
			add(s, next)
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
				start = i
				s, next := quoted(src, j, '"')
				add(s, next)
				i = next
			case j < len(src) && src[j] == '"' && slices.Contains([]string{"R", "LR", "uR", "UR", "u8R"}, word):
				start = i
				s, next := raw(src, j)
				add(s, next)
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
