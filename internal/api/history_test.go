package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// A player's playtime on all servers adds up only the servers whose players
// the caller sees, and isn't there for someone who sees just the one.
func TestHistoryPlaytimeAll(t *testing.T) {
	p, _ := newServersPanel(t, nil, "", "a", "b", "c")
	st, ctx := p.api.Store, context.Background()
	const alice = "76561198000000001"
	for i, id := range []string{"a", "b", "c"} {
		joined := int64(1000 * (i + 1))
		if err := st.SeenPlayer(ctx, id, alice, "Alice", joined); err != nil {
			t.Fatal(err)
		}
		if err := st.LeftPlayer(ctx, id, alice, joined, joined+int64(60*(i+1)), "Disconnected."); err != nil {
			t.Fatal(err)
		}
	}
	all := func() *int64 {
		t.Helper()
		code, b := p.do("GET", "/api/instances/a/history", "")
		var rows []store.PlayerRecord
		if code != 200 || json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].Playtime == nil || *rows[0].Playtime != 60 {
			t.Fatalf("history: %d %s", code, b)
		}
		return rows[0].PlaytimeAll
	}
	if got := all(); got == nil || *got != 360 {
		t.Errorf("the owner's all-servers playtime %v, want 360", got)
	}
	mod := p.roleID("Moderator")
	p.createUser("two", fmt.Sprintf(`{"roleId":%d,"instance":"a"}`, mod), fmt.Sprintf(`{"roleId":%d,"instance":"b"}`, mod))
	p.createUser("one", fmt.Sprintf(`{"roleId":%d,"instance":"a"}`, mod))
	p.signIn("two")
	if got := all(); got == nil || *got != 180 {
		t.Errorf("on a and b: %v, want 180", got)
	}
	p.signIn("one")
	if got := all(); got != nil {
		t.Errorf("on a alone: %d, want none", *got)
	}
}
