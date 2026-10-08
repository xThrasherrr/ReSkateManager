package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// The Logs tab reads the server's log and its rotated copies, by time range,
// filter and search; export and file downloads are audited.
func TestServerLogs(t *testing.T) {
	p, dirs := newServersPanel(t, nil, "", "a")
	dir := dirs["a"]
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local)
	at := func(h, m int) string {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Format("2006-01-02 15:04:05")
	}
	writeFile(t, filepath.Join(dir, "ReSkateServer.log.1"), fmt.Sprintf("[%s] Lobby is up on San Vansterdam for 16 players.\n[%s] Alice joined (76561198000000001), 1/16 players\n", at(10, 0), at(10, 5)))
	writeFile(t, filepath.Join(dir, "ReSkateServer.log"), fmt.Sprintf(
		"[%s] Bob joined (76561198000000002), 2/16 players\n[%s] [chat] Bob: hello\n[%s] 2 players\n  76561198000000001  Alice\n  76561198000000002  Bob\n[%s] Alice left (Timed out)\n",
		at(34, 0), at(34, 1), at(34, 2), at(34, 3)))
	writeFile(t, filepath.Join(dir, "console.log"), fmt.Sprintf("[%s] [manager] Starting ReSkateServer\n[%s] CSteamNetworkingSockets: relay ping assert\n[%s] [input] owner> kick 76561198000000001\n",
		at(34, 0), at(34, 1), at(34, 2)))

	type result struct {
		Entries []logparse.Entry
		Total   int
		Files   map[string][]struct{ Name string }
	}
	get := func(query string) result {
		t.Helper()
		code, b := p.do("GET", "/api/instances/a/logs?"+query, "")
		var r result
		if json.Unmarshal(b, &r); code != 200 {
			t.Fatalf("logs?%s: %d %s", query, code, b)
		}
		return r
	}
	unix := func(h int) int64 { return day.Add(time.Duration(h) * time.Hour).Unix() }

	all := get("")
	if all.Total != 6 || all.Entries[0].Kind != logparse.KindReady || all.Entries[5].Kind != logparse.KindLeave {
		t.Fatalf("all: %+v", all)
	}
	if !strings.Contains(all.Entries[4].Text, "\n  76561198000000002  Bob") || all.Entries[4].Seq != 5 {
		t.Errorf("multi-line entry: %+v", all.Entries[4])
	}
	if len(all.Files["server"]) != 2 || all.Files["server"][0].Name != "ReSkateServer.log.1" || len(all.Files["console"]) != 1 {
		t.Errorf("files: %+v", all.Files)
	}
	if r := get(fmt.Sprintf("from=%d", unix(24))); r.Total != 4 {
		t.Errorf("from the second day: %d", r.Total)
	}
	if r := get(fmt.Sprintf("from=%d&to=%d", unix(0), unix(11))); r.Total != 2 {
		t.Errorf("the first day: %d", r.Total)
	}
	if r := get("group=players"); r.Total != 3 {
		t.Errorf("joins and leaves: %d", r.Total)
	}
	if r := get("q=ALICE"); r.Total != 3 {
		t.Errorf("search: %d", r.Total)
	}
	if r := get("limit=2"); r.Total != 6 || len(r.Entries) != 2 || r.Entries[1].Kind != logparse.KindLeave {
		t.Errorf("the newest two: %+v", r)
	}
	if r := get("source=console"); r.Total != 3 || r.Entries[0].Kind != logparse.KindManager || r.Entries[2].Kind != logparse.KindInput || r.Entries[2].Name != "owner" {
		t.Errorf("console: %+v", r)
	}
	if code, _ := p.do("GET", "/api/instances/a/logs?source=manager", ""); code != 400 {
		t.Errorf("unknown source: %d", code)
	}

	code, b := p.do("GET", "/api/instances/a/logs/export?group=players", "")
	if code != 200 || strings.Count(string(b), "\n") != 3 || !strings.HasPrefix(string(b), "["+at(10, 5)+"] Alice joined") {
		t.Errorf("export: %d %q", code, b)
	}
	code, b = p.do("GET", "/api/instances/a/logs/export?source=console", "")
	if code != 200 || !strings.Contains(string(b), "] [input] owner> kick 76561198000000001\n") {
		t.Errorf("console export keeps its marks: %q", b)
	}
	code, b = p.do("GET", "/api/instances/a/logs/files/ReSkateServer.log.1", "")
	if code != 200 || !strings.Contains(string(b), "Alice joined") {
		t.Errorf("file download: %d %s", code, b)
	}
	for _, name := range []string{"ReSkateServer.json", "..%2Fpanel.db", "ReSkateServer.log.4"} {
		if code, _ := p.do("GET", "/api/instances/a/logs/files/"+name, ""); code != 404 {
			t.Errorf("download %s: %d", name, code)
		}
	}

	// A viewer has console.view, so reads and exports them too.
	p.signIn("viewer")
	if code, _ := p.do("GET", "/api/instances/a/logs", ""); code != 200 {
		t.Errorf("viewer: %d", code)
	}
	p.signIn("owner")
	audit, _ := p.auditActions(t)
	for _, want := range []string{"logs.export", "logs.download"} {
		if !strings.Contains(audit, want) {
			t.Errorf("no %s in the audit log: %s", want, audit)
		}
	}
}

// auditActions lists the actions in the audit log, newest first.
func (p *panel) auditActions(t *testing.T) (string, error) {
	t.Helper()
	code, b := p.do("GET", "/api/audit", "")
	if code != http.StatusOK {
		return "", fmt.Errorf("audit: %d", code)
	}
	var entries []struct{ Action string }
	err := json.Unmarshal(b, &entries)
	var out []string
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return strings.Join(out, " "), err
}
