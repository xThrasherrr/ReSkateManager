package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
)

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"  hello  ":              "hello",
		"two\nlines\r\nhere":     "two lines  here",
		"tab\there":              "tab here",
		"ctrl-z\x1a gone":        "ctrl-z gone",
		"nul\x00 and del\x7f":    "nul and del",
		"c1\u0085 too":           "c1 too",
		"bad \xff bytes":         "bad  bytes",
		"héllo 🛹 — ünïcode":      "héllo 🛹 — ünïcode",
		"family 👨\u200d👩\u200d👧": "family 👨\u200d👩\u200d👧", // joiners stay
	} {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cut("aé", 2); got != "a" {
		t.Errorf("cut through a character: %q", got)
	}
}

// A panic in background work fails that job, not the manager.
func TestJobPanicFailsTheJob(t *testing.T) {
	a := &API{Log: slog.New(slog.DiscardHandler)}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, &caller{user: &auth.User{ID: 1}}))
	w := httptest.NewRecorder()
	a.startJob(w, r, func(context.Context, func(done, total int64)) (any, error) {
		var m map[string]int
		m["boom"] = 1 //lint:ignore SA5000 the panic is what this tests
		return nil, nil
	})
	var started struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &started)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		a.jobs.mu.Lock()
		j := a.jobs.all[started.ID]
		a.jobs.mu.Unlock()
		j.mu.Lock()
		st := j.st
		j.mu.Unlock()
		if st.Phase == "failed" && st.Error != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %+v", st)
		}
	}
}
