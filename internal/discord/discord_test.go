package discord

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

const botID = "1045678901234567890"

var token = base64.RawStdEncoding.EncodeToString([]byte(botID)) + ".GabcDE.abcdefghijklmnopqrstuvwxyz0123"

const channel = "111111111111111111"

func TestCleanToken(t *testing.T) {
	for in, want := range map[string]string{token: token, " Bot " + token + "\n": token} {
		if got, err := CleanToken(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "abc", "0123456789abcdef0123456789abcdef", botID, token + " extra", "a.b.c"} {
		if _, err := CleanToken(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestCleanChannel(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		channel:             channel,
		" " + channel + " ": channel,
		"https://discord.com/channels/222222222222222222/" + channel:                             channel,
		"https://ptb.discord.com/channels/222222222222222222/" + channel + "/333333333333333333": channel,
	} {
		if got, err := CleanChannel(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"general", "#status", "1234", "https://evil.com/channels/2/" + channel, "https://discord.com/channels/2"} {
		if _, err := CleanChannel(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{"": "", "  baked. servers ": "baked. servers", "two\nlines\t here": "two lines here"} {
		if got, err := CleanTitle(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	if _, err := CleanTitle(strings.Repeat("x", MaxTitle+1)); err == nil {
		t.Error("a title over the limit passed")
	}
}

func TestInvite(t *testing.T) {
	want := "https://discord.com/oauth2/authorize?client_id=" + botID + "&scope=bot&permissions=19456"
	if got := Invite(token); got != want {
		t.Errorf("Invite = %q, want %q", got, want)
	}
	if got := Invite(""); got != "" {
		t.Errorf("no token: %q", got)
	}
	reset := strings.Split(token, ".")[0] + ".HxyzAB.zyxwvutsrqponmlkjihgfedcba9876"
	if BotID(token) != botID || BotID(reset) != botID || BotID("not.a.token") != "" {
		t.Errorf("BotID: %q, %q", BotID(token), BotID(reset))
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if set, err := Load(ctx, s); err != nil || set.Token != "" || set.Channel != "" || !set.JoinCodes {
		t.Fatalf("nothing saved: %+v, %v", set, err)
	}
	want := Settings{Token: token, Channel: channel}
	if err := Save(ctx, s, want); err != nil {
		t.Fatal(err)
	}
	if set, _ := Load(ctx, s); set != want {
		t.Errorf("loaded %+v", set)
	}
	for _, bad := range []Settings{{Token: "nope"}, {Channel: "general"}, {Title: strings.Repeat("x", MaxTitle+1)}} {
		if err := Save(ctx, s, bad); err == nil {
			t.Errorf("saved %+v", bad)
		}
	}
}

func running(name string) server {
	now := time.Now()
	return server{Name: name, State: instance.Running, Players: 12,
		ReadyAt: now.Add(-2 * time.Hour).UnixMilli(), NextRestart: now.Add(4 * time.Hour).UnixMilli(),
		Info: instance.Info{Map: "San Vansterdam", MaxPlayers: 32, JoinCode: "ABCD-EFGH"}, Release: "2.0.1"}
}

func TestRender(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	locked := running("Private *VIP*")
	locked.Info.Password = true
	list := []server{
		running("Lobby"),
		locked,
		{Name: "Old", State: instance.Stopped, Info: instance.Info{JoinCode: "STALE"}},
		{Name: "Broken", State: instance.Crashed},
	}
	e := render(list, Defaults, false, now)
	if e.Color != yellow || !strings.Contains(e.Description, "2 of 4 servers online") || !strings.Contains(e.Description, "24 players on") {
		t.Errorf("summary %q, colour %x", e.Description, e.Color)
	}
	if len(e.Fields) != 4 {
		t.Fatalf("%d fields", len(e.Fields))
	}
	lobby := e.Fields[0]
	for _, want := range []string{"**Online** · 12/32 players · San Vansterdam", "Join code `ABCD-EFGH`", "ReSkate 2.0.1",
		fmt.Sprintf("started <t:%d:R>", list[0].ReadyAt/1000), fmt.Sprintf("restarts <t:%d:R>", list[0].NextRestart/1000)} {
		if !strings.Contains(lobby.Value, want) {
			t.Errorf("Lobby's field lacks %q:\n%s", want, lobby.Value)
		}
	}
	if lobby.Name != "🟢 Lobby" {
		t.Errorf("name %q", lobby.Name)
	}
	if f := e.Fields[1]; f.Name != `🟢 Private \*VIP\*` || !strings.Contains(f.Value, "Join code `ABCD-EFGH` · 🔒 Password required") {
		t.Errorf("passworded server: %q %q", f.Name, f.Value)
	}
	// A stopped server's code from its last run is gone with it.
	if f := e.Fields[2]; f.Name != "⚫ Old" || f.Value != "**Offline**" {
		t.Errorf("stopped server: %q %q", f.Name, f.Value)
	}
	if f := e.Fields[3]; f.Name != "🔴 Broken" || f.Value != "**Crashed**" {
		t.Errorf("crashed server: %q %q", f.Name, f.Value)
	}
	if e.Title != "Server status" || e.Footer.Text != "Powered by ReSkateManager" {
		t.Errorf("title %q, footer %q", e.Title, e.Footer.Text)
	}
	if e := render(list, Settings{Title: "**baked.** servers"}, false, now); e.Title != "**baked.** servers" {
		t.Errorf("own title %q", e.Title)
	}

	hidden := render(list, Settings{}, false, now)
	if strings.Contains(hidden.Fields[0].Value, "ABCD") || !strings.Contains(hidden.Fields[1].Value, "🔒 Password required") {
		t.Errorf("codes hidden: %q / %q", hidden.Fields[0].Value, hidden.Fields[1].Value)
	}

	if e := render(list[:2], Defaults, false, now); e.Color != green {
		t.Errorf("all online: colour %x", e.Color)
	}
	if e := render(list[2:], Defaults, false, now); e.Color != red || strings.Contains(e.Description, "players on") {
		t.Errorf("none online: %q, colour %x", e.Description, e.Color)
	}
	if e := render(nil, Defaults, false, now); e.Description != "No servers yet." {
		t.Errorf("no servers: %q", e.Description)
	}
	down := render(list, Defaults, true, now)
	if down.Color != grey || !strings.Contains(down.Description, "<t:1800000000:R>") || strings.Contains(down.Fields[0].Value, "ABCD") || down.Fields[0].Value != "**Offline**" {
		t.Errorf("manager stopped: %q, %q", down.Description, down.Fields[0].Value)
	}
}

func TestRenderLimits(t *testing.T) {
	var list []server
	for i := range 40 {
		list = append(list, running(fmt.Sprintf("Server %02d %s", i, strings.Repeat("x", 50))))
	}
	e := render(list, Defaults, false, time.Now())
	size := len([]rune(e.Title + e.Description + e.Footer.Text))
	for _, f := range e.Fields {
		size += len([]rune(f.Name + f.Value))
	}
	shown := len(e.Fields)
	if shown > maxFields || size > maxSize+100 || !strings.Contains(e.Description, fmt.Sprintf("…and %d more not shown.", 40-shown)) {
		t.Errorf("%d fields, %d characters, %q", shown, size, e.Description)
	}
}

// fakeDiscord answers like Discord's REST API, and records what it was asked.
type fakeDiscord struct {
	mu      sync.Mutex
	calls   []string
	bodies  []string
	auth    []string
	agent   string
	fail    map[string]string // by method: "<status> <body>"
	nextID  int
	noEmbed bool
}

func (f *fakeDiscord) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.bodies = append(f.bodies, string(b))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.agent = r.Header.Get("User-Agent")
	if v, ok := f.fail[r.Method]; ok {
		code, body, _ := strings.Cut(v, " ")
		n, _ := strconv.Atoi(code)
		if n == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "30")
		}
		w.WriteHeader(n)
		io.WriteString(w, body)
		return
	}
	embeds := `[{}]`
	if f.noEmbed {
		embeds = `[]`
	}
	switch r.Method {
	case http.MethodGet:
		fmt.Fprintf(w, `{"id":%q,"name":"status","guild_id":"222222222222222222"}`, path.Base(r.URL.Path))
	case http.MethodPost:
		f.nextID++
		fmt.Fprintf(w, `{"id":"90000000000000000%d","embeds":%s}`, f.nextID, embeds)
	case http.MethodPatch:
		fmt.Fprintf(w, `{"id":%q,"embeds":%s}`, path.Base(r.URL.Path), embeds)
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	}
}

// took returns the calls made since the last time, and forgets them.
func (f *fakeDiscord) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// body is the body of the i-th request, with its Authorization header.
func (f *fakeDiscord) body(i int) (body, auth, agent string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i], f.auth[i], f.agent
}

func (f *fakeDiscord) set(method, answer string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]string{}
	}
	if answer == "" {
		delete(f.fail, method)
	} else {
		f.fail[method] = answer
	}
}

func newPoster(t *testing.T) (*Poster, *fakeDiscord, *instance.Registry) {
	t.Helper()
	fake := &fakeDiscord{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	log := slog.New(slog.DiscardHandler)
	reg := instance.NewRegistry(log)
	reg.Add(instance.Def{ID: "lobby", Name: "Lobby", Dir: t.TempDir()})
	return &Poster{Store: openStore(t), Reg: reg, Log: log, Version: "1.5.0", API: srv.URL}, fake, reg
}

func same(got []string, want ...string) bool {
	return strings.Join(got, "\n") == strings.Join(want, "\n")
}

func TestPoster(t *testing.T) {
	ctx := context.Background()
	p, fake, reg := newPoster(t)
	now := time.Now()

	p.update(ctx, now, false)
	if c := fake.took(); len(c) != 0 || p.Status() != (Status{}) {
		t.Fatalf("no bot set up: %v, %+v", c, p.Status())
	}

	if err := Save(ctx, p.Store, Settings{Token: token, Channel: channel, JoinCodes: true}); err != nil {
		t.Fatal(err)
	}
	st := p.Refresh(ctx)
	msgPath := "/channels/" + channel + "/messages/900000000000000001"
	if c := fake.took(); !same(c, "GET /channels/"+channel, "POST /channels/"+channel+"/messages") {
		t.Fatalf("first post: %v", c)
	}
	if st.Channel != "status" || st.Link != "https://discord.com/channels/222222222222222222/"+channel+"/900000000000000001" || st.Updated == 0 || st.Problem != "" {
		t.Errorf("status after posting: %+v", st)
	}
	body, auth, agent := fake.body(1)
	if auth != "Bot "+token || !strings.HasPrefix(agent, "DiscordBot (https://github.com/xThrasherrr/ReSkateManager, 1.5.0)") {
		t.Errorf("sent as %q, %q", auth, agent)
	}
	var sent message
	if err := json.Unmarshal([]byte(body), &sent); err != nil || len(sent.Embeds) != 1 || sent.AllowedMentions.Parse == nil || len(sent.AllowedMentions.Parse) != 0 {
		t.Errorf("message %s (%v)", body, err)
	}
	if !strings.Contains(sent.Embeds[0].Description, "Updated <t:") || sent.Embeds[0].Fields[0].Name != "⚫ Lobby" {
		t.Errorf("embed %+v", sent.Embeds[0])
	}

	// Nothing changed: no edit until the heartbeat.
	p.update(ctx, now.Add(2*time.Minute), false)
	if c := fake.took(); len(c) != 0 {
		t.Errorf("unchanged, before the heartbeat: %v", c)
	}
	p.update(ctx, now.Add(heartbeat+time.Second), false)
	if c := fake.took(); !same(c, "PATCH "+msgPath) {
		t.Errorf("heartbeat: %v", c)
	}
	now = now.Add(heartbeat + time.Second)

	// A change waits for the minute since the last edit.
	in, _ := reg.Get("lobby")
	d := in.Def()
	d.Name = "Main lobby"
	in.SetDef(d)
	p.update(ctx, now.Add(10*time.Second), false)
	if c := fake.took(); len(c) != 0 {
		t.Errorf("changed within the minute: %v", c)
	}
	p.update(ctx, now.Add(minGap), false)
	if c := fake.took(); !same(c, "PATCH "+msgPath) {
		t.Errorf("changed: %v", c)
	}
	now = now.Add(minGap)

	// Someone deleted the message: post another.
	fake.set(http.MethodPatch, `404 {"message":"Unknown Message","code":10008}`)
	p.Refresh(ctx)
	fake.set(http.MethodPatch, "")
	if c := fake.took(); !same(c, "PATCH "+msgPath, "GET /channels/"+channel, "POST /channels/"+channel+"/messages") {
		t.Errorf("message deleted: %v", c)
	}
	if pst := p.loadPosted(ctx); pst.Message != "900000000000000002" {
		t.Errorf("kept %+v", pst)
	}
	msgPath = "/channels/" + channel + "/messages/900000000000000002"

	// A refused token stops the edits until the settings change.
	fake.set(http.MethodPatch, `401 {"message":"401: Unauthorized","code":0}`)
	st = p.Refresh(ctx)
	if !st.Stopped || !strings.Contains(st.Problem, "token") || st.Channel != "status" {
		t.Errorf("refused: %+v", st)
	}
	fake.took()
	p.update(ctx, now.Add(time.Hour), false)
	if c := fake.took(); len(c) != 0 {
		t.Errorf("tried again while refused: %v", c)
	}
	fake.set(http.MethodPatch, "")
	if st = p.Refresh(ctx); st.Stopped || st.Problem != "" {
		t.Errorf("after the settings change: %+v", st)
	}
	fake.took()

	// A rate limit waits as long as Discord says, even for a change of settings.
	fake.set(http.MethodPatch, `429 {"message":"You are being rate limited.","retry_after":30}`)
	limited := time.Now()
	st = p.Refresh(ctx)
	fake.set(http.MethodPatch, "")
	if st.Stopped || !strings.Contains(st.Problem, "slow down") {
		t.Errorf("rate limited: %+v", st)
	}
	fake.took()
	p.update(ctx, limited.Add(10*time.Second), true)
	if c := fake.took(); len(c) != 0 {
		t.Errorf("within the rate limit: %v", c)
	}
	p.update(ctx, limited.Add(31*time.Second), true)
	if c := fake.took(); !same(c, "PATCH "+msgPath) {
		t.Errorf("after the rate limit: %v", c)
	}

	// Moving to another channel deletes the old message with the old token.
	old, _ := Load(ctx, p.Store)
	p.Retire(ctx, old)
	if c := fake.took(); !same(c, "DELETE "+msgPath) || p.loadPosted(ctx).Message != "" || p.Status() != (Status{}) {
		t.Errorf("retired: %v, %+v", c, p.loadPosted(ctx))
	}
}

func TestPosterProblems(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		method, answer string
		stop           bool
		says           string
	}{
		"no channel":     {http.MethodGet, `404 {"message":"Unknown Channel","code":10003}`, true, "no channel with that ID"},
		"no access":      {http.MethodGet, `403 {"message":"Missing Access","code":50001}`, false, "View Channel, Send Messages and Embed Links"},
		"can't send":     {http.MethodPost, `403 {"message":"Missing Permissions","code":50013}`, false, "Send Messages"},
		"discord broken": {http.MethodPost, `502 bad gateway`, false, "HTTP 502"},
	} {
		t.Run(name, func(t *testing.T) {
			p, fake, _ := newPoster(t)
			Save(ctx, p.Store, Settings{Token: token, Channel: channel})
			fake.set(c.method, c.answer)
			st := p.Refresh(ctx)
			if st.Stopped != c.stop || !strings.Contains(st.Problem, c.says) || st.Updated != 0 {
				t.Errorf("status %+v", st)
			}
		})
	}

	// Without Embed Links the message goes out bare; it's kept, and says so.
	p, fake, _ := newPoster(t)
	Save(ctx, p.Store, Settings{Token: token, Channel: channel})
	fake.mu.Lock()
	fake.noEmbed = true
	fake.mu.Unlock()
	st := p.Refresh(ctx)
	if st.Stopped || !strings.Contains(st.Problem, "Embed Links") || st.Updated == 0 || p.loadPosted(ctx).Message == "" {
		t.Errorf("no embed: %+v", st)
	}
}

// No access to the channel is put right in Discord, such as by inviting the
// bot, so it's tried again, less and less often, until it works.
func TestPosterBackoff(t *testing.T) {
	ctx := context.Background()
	p, fake, _ := newPoster(t)
	Save(ctx, p.Store, Settings{Token: token, Channel: channel})
	fake.set(http.MethodGet, `403 {"message":"Missing Access","code":50001}`)
	t0 := time.Now()
	get := "GET /channels/" + channel
	tries := func(at time.Duration, want ...string) {
		t.Helper()
		p.update(ctx, t0.Add(at), false)
		if c := fake.took(); !same(c, want...) {
			t.Errorf("at %s: %v, want %v", at, c, want)
		}
	}
	tries(0, get)
	if st := p.Status(); st.Stopped || st.Problem == "" {
		t.Errorf("refused: %+v", st)
	}
	tries(30 * time.Second)     // a minute's wait first,
	tries(61*time.Second, get)  // then
	tries(150 * time.Second)    // two minutes',
	tries(182*time.Second, get) // then
	tries(400 * time.Second)    // four
	// Try again, from the panel, doesn't wait, and starts the waits over.
	p.Refresh(ctx)
	if c := fake.took(); !same(c, get) {
		t.Errorf("tried again by hand: %v", c)
	}
	fake.set(http.MethodGet, "") // the bot was invited
	tries(time.Minute+time.Second, get, "POST /channels/"+channel+"/messages")
	if st := p.Status(); st.Problem != "" || st.Updated == 0 || p.refused != 0 {
		t.Errorf("once invited: %+v", st)
	}
}

func TestFarewell(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, fake, _ := newPoster(t)
	Save(ctx, p.Store, Settings{Token: token, Channel: channel})
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for p.Status().Updated == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	c := fake.took()
	if len(c) != 3 || c[2] != "PATCH /channels/"+channel+"/messages/900000000000000001" {
		t.Fatalf("calls %v", c)
	}
	if body, _, _ := fake.body(2); !strings.Contains(body, "The manager stopped") {
		t.Errorf("last edit %s", body)
	}
}

func TestTest(t *testing.T) {
	ctx := context.Background()
	p, fake, _ := newPoster(t)
	if name, err := p.Test(ctx, token, channel); err != nil || name != "status" {
		t.Errorf("Test = %q, %v", name, err)
	}
	if c := fake.took(); !same(c, "GET /channels/"+channel, "POST /channels/"+channel+"/messages") {
		t.Errorf("calls %v", c)
	}
	// Nothing is saved: the status message is still to be posted.
	if pst := p.loadPosted(ctx); pst.Message != "" {
		t.Errorf("kept %+v", pst)
	}
	fake.set(http.MethodGet, `401 {"message":"401: Unauthorized","code":0}`)
	if _, err := p.Test(ctx, token, channel); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("refused token: %v", err)
	}
}
