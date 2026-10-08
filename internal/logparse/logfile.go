package logparse

import (
	"bufio"
	"io"
	"regexp"
	"strings"
	"time"
)

// ReSkateServer.log stamps lines with the date as well: "[2006-01-02 15:04:05] text".
var fileStampRe = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})\] ?(.*)$`)

const fileStamp = "2006-01-02 15:04:05"

// Scan reads a log in ReSkateServer.log's format and calls fn with each
// entry, its At from the line's stamp and its text as written, not yet
// classified. Lines before the first stamped one (a cut-off start) are
// dropped. fn returns false to stop.
func Scan(r io.Reader, fn func(Entry) bool) error {
	var cur *Entry
	var lines []string
	emit := func() bool {
		if cur == nil {
			return true
		}
		e := *cur
		e.Text = strings.Join(lines, "\n")
		cur = nil
		return fn(e)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		m := fileStampRe.FindStringSubmatch(line)
		if m == nil {
			if cur != nil {
				lines = append(lines, line)
			}
			continue
		}
		if !emit() {
			return nil
		}
		at, _ := time.ParseInLocation(fileStamp, m[1]+" "+m[2], time.Local)
		cur = &Entry{Stamp: m[2], At: at.UnixMilli()}
		lines = append(lines[:0], m[3])
	}
	emit()
	return sc.Err()
}

// ReadLog parses ReSkateServer.log into entries and returns the last max.
func ReadLog(r io.Reader, max int) []Entry {
	var out []Entry
	Scan(r, func(e Entry) bool {
		Classify(&e)
		out = append(out, e)
		if len(out) > 2*max {
			out = append(out[:0], out[len(out)-max:]...)
		}
		return true
	})
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// console.log, which the manager writes, holds the console's own lines too,
// marked so they read back as what they were. A server line that starts like
// a mark, as a join line does for a player named "[input] owner> ...", gets
// one of its own, so no player can write a command into the log as someone
// else's.
const (
	managerMark = "[manager] "
	inputMark   = "[input] "
	serverMark  = "[server] "
)

func marked(text string) bool {
	return strings.HasPrefix(text, managerMark) || strings.HasPrefix(text, inputMark) || strings.HasPrefix(text, serverMark)
}

// FormatLine writes e as it goes in a log file: stamped with its date and
// time like ReSkateServer.log, the lines after its first as they are. The
// manager's notes and the commands sent are marked.
func FormatLine(e Entry) string {
	text := e.Text
	switch e.Kind {
	case KindManager:
		text = managerMark + text
	case KindInput:
		text = inputMark + e.Name + "> " + text
	default:
		if marked(text) {
			text = serverMark + text
		}
	}
	return "[" + time.UnixMilli(e.At).Format(fileStamp) + "] " + text + "\n"
}

// ClassifyConsole is Classify for an entry read back from console.log.
func ClassifyConsole(e *Entry) {
	if text, ok := strings.CutPrefix(e.Text, serverMark); ok {
		e.Text = text
		Classify(e)
		return
	}
	if text, ok := strings.CutPrefix(e.Text, managerMark); ok {
		e.Kind, e.Text = KindManager, text
		return
	}
	if rest, ok := strings.CutPrefix(e.Text, inputMark); ok {
		if name, text, ok := strings.Cut(rest, "> "); ok {
			e.Kind, e.Name, e.Text = KindInput, name, text
			return
		}
	}
	Classify(e)
}
