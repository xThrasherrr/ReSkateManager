package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
)

// A server's restart schedule is checked, tidied and kept; changing it takes
// instances.manage.
func TestRestartSchedule(t *testing.T) {
	p, _ := newServersPanel(t, nil, "", "a")
	code, b := p.do("PATCH", "/api/instances/a", `{"restartTimes":["16:30"," 4:00","04:00"],"restartHours":6}`)
	var v struct {
		RestartTimes []string
		RestartHours int
	}
	if json.Unmarshal(b, &v); code != 200 || strings.Join(v.RestartTimes, ",") != "04:00,16:30" || v.RestartHours != 6 {
		t.Fatalf("set: %d %s", code, b)
	}
	for _, bad := range []string{`{"restartTimes":["25:00"]}`, `{"restartTimes":["noon"]}`, `{"restartHours":-1}`, `{"restartHours":1000}`} {
		if code, b := p.do("PATCH", "/api/instances/a", bad); code != 400 {
			t.Errorf("%s: %d %s, want 400", bad, code, b)
		}
	}
	_, b = p.do("GET", "/api/instances", "")
	if !strings.Contains(string(b), `"restartTimes":["04:00","16:30"]`) || !strings.Contains(string(b), `"restartHours":6`) {
		t.Errorf("not kept: %s", b)
	}
	if code, b := p.do("PATCH", "/api/instances/a", `{"restartTimes":[],"restartHours":0}`); code != 200 || !strings.Contains(string(b), `"restartTimes":[]`) {
		t.Errorf("clear: %d %s", code, b)
	}
	p.signIn("viewer")
	if code, _ := p.do("PATCH", "/api/instances/a", `{"restartHours":2}`); code != 403 {
		t.Errorf("viewer: %d, want 403", code)
	}
}

// Owners set how long the audit log and player history are kept.
func TestRetention(t *testing.T) {
	p, _ := newServersPanel(t, nil, "", "a")
	code, b := p.do("GET", "/api/manager/retention", "")
	if code != 200 || !strings.Contains(string(b), `"auditDays":90`) || !strings.Contains(string(b), `"playerDays":180`) {
		t.Fatalf("default retention: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/manager/retention", `{"auditDays":30}`); code != 200 || !strings.Contains(string(b), `"auditDays":30,"playerDays":180`) {
		t.Errorf("save: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/manager/retention", `{"playerDays":-5}`); code != 400 {
		t.Errorf("negative days: %d %s", code, b)
	}
	if _, b := p.do("GET", "/api/audit", ""); !strings.Contains(string(b), `"manager.retention"`) {
		t.Errorf("audit: %s", b)
	}
	p.signIn("viewer")
	if code, _ := p.do("GET", "/api/manager/retention", ""); code != 403 {
		t.Errorf("viewer: %d, want 403", code)
	}
}

// Owners set where alerts go and which are sent.
func TestManagerAlerts(t *testing.T) {
	p, _ := newServersPanel(t, nil, "", "a")
	code, b := p.do("GET", "/api/manager/alerts", "")
	var set struct {
		Webhook                    string
		Off                        []string
		Kinds                      []string
		DiskGB, MemPct, MemMinutes int
	}
	if json.Unmarshal(b, &set); code != 200 || set.Webhook != "" || !slices.Equal(set.Kinds, alerts.Kinds) || set.DiskGB != 10 || set.MemPct != 90 || set.MemMinutes != 10 {
		t.Fatalf("alerts: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/manager/alerts", `{"webhook":"https://example.com/hook"}`); code != 400 {
		t.Errorf("a webhook not on Discord: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/manager/alerts", `{"off":["everything"]}`); code != 400 {
		t.Errorf("an unknown kind: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/manager/alerts", `{"memPct":100}`); code != 400 {
		t.Errorf("memory at 100%%: %d %s", code, b)
	}
	hook := "https://discord.com/api/webhooks/123/abc"
	if code, b := p.do("PATCH", "/api/manager/alerts", `{"webhook":" `+hook+` ","off":["updated"],"diskGB":25,"memMinutes":5}`); code != 200 {
		t.Fatalf("save: %d %s", code, b)
	}
	_, b = p.do("GET", "/api/manager/alerts", "")
	if json.Unmarshal(b, &set); set.Webhook != hook || strings.Join(set.Off, ",") != "updated" || set.DiskGB != 25 || set.MemPct != 90 || set.MemMinutes != 5 {
		t.Errorf("not kept: %s", b)
	}
	if code, b := p.do("POST", "/api/manager/alerts/test", `{"webhook":"https://evil.example/api/webhooks/1/x"}`); code != 400 {
		t.Errorf("test to another site: %d %s", code, b)
	}
	if _, b := p.do("GET", "/api/audit", ""); !strings.Contains(string(b), `"manager.alerts"`) {
		t.Errorf("audit: %s", b)
	}
	p.signIn("viewer")
	if code, _ := p.do("GET", "/api/manager/alerts", ""); code != 403 {
		t.Errorf("viewer: %d, want 403", code)
	}
}
