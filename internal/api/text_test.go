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
