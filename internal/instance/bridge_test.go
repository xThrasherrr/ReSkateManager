package instance

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// A ReSkate plugin's messages reach the manager's plugins only with the key
// its hello was answered with: never before it, never with another
// plugin's, and each hello only once in helloEvery.
func TestBridgeKeys(t *testing.T) {
	in := New(Def{ID: "x", Name: "X", Dir: t.TempDir()}, slog.New(slog.DiscardHandler))
	var got []Notice
	in.OnNotice = func(n Notice) {
		if n.Kind == NoticeReSkate {
			got = append(got, n)
		}
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.state = Running
	line := func(text string) {
		e := logparse.Entry{Text: text}
		logparse.Classify(&e)
		in.handleLocked(e)
	}
	msg := func(id, key, data string) string {
		return "[Plugins] [" + id + "] rsm " + key + " " + base64.StdEncoding.EncodeToString([]byte(data))
	}
	const guess = "AAAAAAAAAAAAAAAAAAAAAAAAAA"

	line(msg("races", guess, `{"n":1}`)) // before its hello
	line("[Plugins] [races] rsm hello")
	line("[Plugins] [races] rsm hello") // again at once
	key := in.bridge["races"].key
	if key == "" {
		t.Fatal("no key after the hello")
	}
	line(msg("races", key, `{"n":2}`))
	line(msg("races", guess, `{"n":3}`))
	line(msg("hide", key, `{"n":4}`)) // another plugin's key
	line(msg("races", key, `{"n":5}`) + "\nand a line after it")

	want := []Notice{{Kind: NoticeReSkate, Plugin: "races"}, {Kind: NoticeReSkate, Plugin: "races", Text: `{"n":2}`}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("heard %+v, want %+v", got, want)
	}
	var notes, shown int
	for _, e := range in.console {
		if e.Kind == logparse.KindManager && strings.HasPrefix(e.Text, "Dropped a message from the server plugin ") {
			notes++
		}
		if strings.Contains(e.Text, " rsm ") {
			shown++
		}
	}
	// One note for races (before its hello, then a guess), one for hide.
	// Shown: the two hellos, and the line that's no message.
	if notes != 2 || shown != 3 {
		t.Errorf("%d notes and %d lines shown, want 2 and 3", notes, shown)
	}

	for i := range bridgeMost + 10 {
		line(fmt.Sprintf("[Plugins] [p%d] rsm hello", i))
	}
	if len(in.bridge) != bridgeMost {
		t.Errorf("keys kept for %d plugins, want %d", len(in.bridge), bridgeMost)
	}
}

// The plugin gets its key through its console command, out of the console's
// view, and its messages come through with it. A new run takes none of the
// old keys.
func TestBridgeHandshake(t *testing.T) {
	in := New(fakeServer(t), slog.New(slog.DiscardHandler))
	got := make(chan Notice, 64) // OnNotice must never wait
	in.OnNotice = func(n Notice) {
		if n.Kind == NoticeReSkate {
			got <- n
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() { in.Stop(ctx) })
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	command := func(line string) string {
		t.Helper()
		reply, err := in.Command(ctx, line, "test")
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return reply
	}
	hear := func() Notice {
		t.Helper()
		select {
		case n := <-got:
			return n
		case <-time.After(5 * time.Second):
			t.Fatal("heard nothing")
		}
		return Notice{}
	}

	command("emit [Plugins] [races] rsm hello")
	if n := hear(); n.Plugin != "races" || n.Text != "" {
		t.Fatalf("hello heard as %+v", n)
	}
	for i := 0; command(`bridge races {"won":"Bea"}`) == "no key"; i++ {
		if i == 50 {
			t.Fatal("the plugin never got its key")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := hear(); n.Plugin != "races" || n.Text != `{"won":"Bea"}` {
		t.Fatalf("message heard as %+v", n)
	}
	in.mu.Lock()
	key := in.bridge["races"].key
	for _, e := range in.console {
		if strings.Contains(e.Text, key) || strings.HasPrefix(e.Text, "rsm ") {
			t.Errorf("in the console: %q", e.Text)
		}
	}
	in.mu.Unlock()

	if err := in.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	waitState(t, in, Running)
	command("emit [Plugins] [races] rsm " + key + " " + base64.StdEncoding.EncodeToString([]byte(`{"won":"Zee"}`)))
	select {
	case n := <-got:
		t.Errorf("the old key still carried %+v", n)
	case <-time.After(300 * time.Millisecond):
	}
}
