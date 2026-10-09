package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// usage is the text in the manager's code that may come from the server:
// the lines it matches, the commands it sends, the files it reads.
type usage struct {
	frags map[string][]string // meaningful pieces, by where they are (path:line)
	words map[string]bool     // every piece, however short
	runs  map[string]bool     // the pieces of regular expressions, such as the log parser's tags
	parse map[string]bool     // the pieces in the log parser (internal/logparse)
}

// managerStrings collects the string literals of the manager's Go code (its
// tests and this command aside). A regular expression gives its literal
// runs, so `^(.+) joined \((\d+)` gives " joined (", and a format string the
// parts between its verbs.
func managerStrings(root string) (*usage, error) {
	u := &usage{frags: map[string][]string{}, words: map[string]bool{}, runs: map[string]bool{}, parse: map[string]bool{}}
	fset := token.NewFileSet()
	self, _ := filepath.Abs(filepath.Join(root, "cmd", "reskate-watch"))
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if abs, _ := filepath.Abs(p); d.Name() == "testdata" || abs == self {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			u.addFile(fset, f, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return u, nil
}

// sends reports whether the manager has text for the console command verb:
// the verb alone, or the verb and its arguments.
func (u *usage) sends(verb string) bool {
	for w := range u.words {
		if w == verb || strings.HasPrefix(w, verb+" ") {
			return true
		}
	}
	return false
}

// sendsForm reports whether the manager has text for one form of a command:
// its words up to the first that varies, so "votes polls" of "votes polls
// off|admins|everyone". A form that varies from its second word can't be told
// from the command's others.
func (u *usage) sendsForm(form string) bool {
	var fixed []string
	for w := range strings.FieldsSeq(form) {
		if strings.ContainsAny(w, "<[|") {
			break
		}
		fixed = append(fixed, w)
	}
	return len(fixed) > 1 && u.sends(strings.Join(fixed, " "))
}

// knowsTag reports whether the manager knows the console tag t: from a
// regular expression (the log parser's alternation of tags) or text that
// has it in brackets. A plain "steam" elsewhere doesn't count.
func (u *usage) knowsTag(t string) bool {
	if u.runs[t] {
		return true
	}
	for w := range u.words {
		if strings.Contains(w, "["+t+"]") {
			return true
		}
	}
	return false
}

// starts reports whether the log parser knows a line that starts with s:
// text of its own, long enough to tell lines apart, begins it.
func (u *usage) starts(s string) bool {
	for w := range u.parse {
		if len(strings.TrimSpace(w)) < 5 || !meaningful(w) || !strings.HasPrefix(s, w) {
			continue
		}
		// A whole word of it: "steam" doesn't start "steam_debug: ...".
		if len(s) == len(w) || !isIdent(w[len(w)-1]) || !isIdent(s[len(w)]) {
			return true
		}
	}
	return false
}

var fmtVerb = regexp.MustCompile(`%[-+# 0]*[0-9*]*(?:\.[0-9*]+)?[a-zA-Z%]`)

func (u *usage) addFile(fset *token.FileSet, f *ast.File, rel string) {
	patterns := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "MustCompile" || sel.Sel.Name == "Compile") {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "regexp" {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok {
					patterns[lit] = true
				}
			}
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		parts := fmtVerb.Split(s, -1)
		if patterns[lit] {
			parts = regexRuns(s)
		}
		where := fmt.Sprintf("%s:%d", rel, fset.Position(lit.Pos()).Line)
		for _, p := range parts {
			u.words[p] = true
			u.runs[p] = u.runs[p] || patterns[lit]
			if strings.HasPrefix(rel, "internal/logparse/") {
				u.parse[p] = true
			}
			if meaningful(p) {
				u.frags[p] = append(u.frags[p], where)
			}
		}
		return true
	})
}

// regexRuns splits a regular expression into the runs of literal text it
// matches: a group, a class, an escape like \d or an alternation ends a run,
// and a quantifier takes the character before it off.
func regexRuns(re string) []string {
	var runs []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			runs = append(runs, string(cur))
		}
		cur = cur[:0]
	}
	for i := 0; i < len(re); i++ {
		switch c := re[i]; c {
		case '\\':
			if i+1 < len(re) {
				i++
				if e := re[i]; !isIdent(e) {
					cur = append(cur, e) // \( \. \[ ...
					continue
				}
			}
			flush() // \d \s \b ...
		case '?', '*', '+', '{':
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
			flush()
			if c == '{' {
				if end := strings.IndexByte(re[i:], '}'); end > 0 {
					i += end
				}
			}
		case '[':
			flush()
			j := i + 1
			if j < len(re) && re[j] == '^' {
				j++
			}
			if j < len(re) && re[j] == ']' {
				j++
			}
			for j < len(re) && re[j] != ']' {
				if re[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case '(':
			flush()
			if strings.HasPrefix(re[i:], "(?") {
				// (?: (?i) (?P<name> (?<name>: past the group's flags or name
				for i+1 < len(re) && re[i+1] != ':' && re[i+1] != ')' && re[i+1] != '>' {
					i++
				}
				i++
			}
		case ')', '|', '.', '^', '$':
			flush()
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return runs
}
