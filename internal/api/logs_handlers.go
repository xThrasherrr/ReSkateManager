package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// The Logs tab reads a server's log files: its own ReSkateServer.log, or the
// console.log the manager keeps, each with its rotated copies.
var logSources = map[string]string{"server": instance.ServerLog, "console": instance.ConsoleLog}

// logGroups are the console's filters, by the same names.
var logGroups = map[string]func(e *logparse.Entry) bool{
	"all": func(*logparse.Entry) bool { return true },
	"chat": func(e *logparse.Entry) bool {
		return e.Kind == logparse.KindChat || e.Kind == logparse.KindPartyChat || e.Kind == logparse.KindCommand
	},
	"players":   func(e *logparse.Entry) bool { return e.Kind == logparse.KindJoin || e.Kind == logparse.KindLeave },
	"admin":     func(e *logparse.Entry) bool { return e.Kind == logparse.KindAdmin || e.Kind == logparse.KindInput },
	"anticheat": func(e *logparse.Entry) bool { return e.Tag == "anticheat" },
	"manager":   func(e *logparse.Entry) bool { return e.Kind == logparse.KindManager },
}

const (
	logShow    = 5000 // lines the Logs tab gets at most, the newest that match
	logShowMax = 20000
	// logScans caps the log queries and exports running at once, across
	// servers: each reads whole files, which can be tens of MB.
	logScans  = 4
	maxSearch = 200 // characters
)

// scanSlot waits for room to scan logs, or for the request to give up.
func (a *API) scanSlot(r *http.Request) (release func(), err error) {
	a.scansOnce.Do(func() { a.scans = make(chan struct{}, logScans) })
	select {
	case a.scans <- struct{}{}:
		return func() { <-a.scans }, nil
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

// logQuery is what the Logs tab asks for: a source, a time range, a filter
// and a search.
type logQuery struct {
	source   string
	file     string
	from, to int64 // unix ms; 0 leaves that end open
	group    string
	search   string // lowercased
}

func parseLogQuery(r *http.Request) (logQuery, error) {
	v := r.URL.Query()
	q := logQuery{source: v.Get("source"), group: v.Get("group"), search: strings.ToLower(strings.TrimSpace(v.Get("q")))}
	if utf8.RuneCountInString(q.search) > maxSearch {
		return q, fmt.Errorf("search for at most %d characters", maxSearch)
	}
	if q.source == "" {
		q.source = "server"
	}
	file, ok := logSources[q.source]
	if !ok {
		return q, errors.New("the source is server or console")
	}
	q.file = file
	if q.group == "" {
		q.group = "all"
	}
	if _, ok := logGroups[q.group]; !ok {
		return q, fmt.Errorf("unknown filter %q", q.group)
	}
	for _, p := range []struct {
		key string
		to  *int64
	}{{"from", &q.from}, {"to", &q.to}} {
		if s := v.Get(p.key); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 || n > math.MaxInt64/1000 {
				return q, fmt.Errorf("%s is a time in unix seconds", p.key)
			}
			*p.to = n * 1000
		}
	}
	if q.from > 0 && q.to > 0 && q.from > q.to {
		return q, errors.New("the range ends before it starts")
	}
	return q, nil
}

// describe sums the query up for the audit log.
func (q logQuery) describe() string {
	stamp := func(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04") }
	out := q.file
	switch {
	case q.from > 0 && q.to > 0:
		out += ", " + stamp(q.from) + " to " + stamp(q.to)
	case q.from > 0:
		out += ", from " + stamp(q.from)
	case q.to > 0:
		out += ", until " + stamp(q.to)
	}
	if q.group != "all" {
		out += ", " + q.group
	}
	if q.search != "" {
		out += ", search " + strconv.Quote(q.search)
	}
	return out
}

// scanLogs calls fn with each entry in the query's files, oldest first,
// that it matches: as read, to write back out, and classified, to show.
func scanLogs(ctx context.Context, dir string, q logQuery, fn func(raw, shown logparse.Entry)) error {
	classify := logparse.Classify
	if q.file == instance.ConsoleLog {
		classify = logparse.ClassifyConsole
	}
	group := logGroups[q.group]
	for _, lf := range instance.LogFiles(dir, q.file) {
		// Every line in a file was written before it last changed.
		if q.from > 0 && lf.Modified.UnixMilli() < q.from {
			continue
		}
		f, err := instance.OpenLog(dir, lf)
		if errors.Is(err, os.ErrNotExist) {
			continue // rotated away meanwhile
		}
		if err != nil {
			return err
		}
		err = logparse.Scan(f, func(e logparse.Entry) bool {
			if ctx.Err() != nil {
				return false
			}
			if (q.from > 0 && e.At < q.from) || (q.to > 0 && e.At >= q.to) {
				return true
			}
			shown := e
			classify(&shown)
			if !group(&shown) {
				return true
			}
			if q.search != "" && !strings.Contains(strings.ToLower(shown.Text), q.search) && !strings.Contains(strings.ToLower(shown.Name), q.search) {
				return true
			}
			fn(e, shown)
			return true
		})
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", lf.Name, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// logs answers the newest entries that match the query, how many match in
// all, and the log files there are.
func (a *API) logs(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	q, err := parseLogQuery(r)
	if err != nil {
		a.fail(w, r, 400, err)
		return
	}
	limit := logShow
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, logShowMax)
	}
	release, err := a.scanSlot(r)
	if err != nil {
		return // the request gave up
	}
	defer release()
	// The newest limit, in a ring.
	ring := make([]logparse.Entry, 0, min(limit, 1024))
	start, total := 0, 0
	err = scanLogs(r.Context(), in.Def().Dir, q, func(_, e logparse.Entry) {
		total++
		if len(ring) < limit {
			ring = append(ring, e)
			return
		}
		ring[start] = e
		start = (start + 1) % limit
	})
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	entries := append(ring[start:], ring[:start]...)
	for i := range entries {
		entries[i].Seq = uint64(i + 1)
	}
	files := map[string][]instance.LogFile{}
	for source, name := range logSources {
		files[source] = instance.LogFiles(in.Def().Dir, name)
	}
	writeJSON(w, 200, map[string]any{"entries": entries, "total": total, "files": files})
}

// exportLogs answers every entry that matches the query, as lines of a log
// file to download.
func (a *API) exportLogs(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	q, err := parseLogQuery(r)
	if err != nil {
		a.fail(w, r, 400, err)
		return
	}
	release, err := a.scanSlot(r)
	if err != nil {
		return // the request gave up
	}
	defer release()
	a.audit(r, in.ID(), "logs.export", q.describe())
	name := fmt.Sprintf("%s-%s-%s.log", in.ID(), q.source, time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	attachment(w, name)
	bw := bufio.NewWriterSize(w, 64<<10)
	err = scanLogs(r.Context(), in.Def().Dir, q, func(raw, _ logparse.Entry) {
		bw.WriteString(logparse.FormatLine(raw))
	})
	if err == nil {
		err = bw.Flush()
	}
	if err != nil && r.Context().Err() == nil {
		// Too late for an error; the download ends short.
		a.Log.Warn("log export cut short", "instance", in.ID(), "err", err)
	}
}

// downloadLog answers one of the server's log files whole.
func (a *API) downloadLog(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	name, _ := pathParam(r, "file") // a name no log file has is refused below
	var file *instance.LogFile
	for _, source := range logSources {
		for _, lf := range instance.LogFiles(in.Def().Dir, source) {
			if lf.Name == name {
				file = &lf
			}
		}
	}
	if file == nil {
		writeErr(w, 404, "no such log file")
		return
	}
	f, err := instance.OpenLog(in.Def().Dir, *file)
	if err != nil {
		writeErr(w, 404, "no such log file")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if wholeDownload(r) {
		a.audit(r, in.ID(), "logs.download", name)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	attachment(w, in.ID()+"-"+name)
	http.ServeContent(w, r, name, fi.ModTime(), f)
}
