package plugins

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
	"github.com/xThrasherrr/ReSkateManager/internal/supervisor"
)

// Servers is where plugins find the servers they read and act on: the
// manager's registry.
type Servers interface {
	Get(id string) (*instance.Instance, bool)
	List() []*instance.Instance
}

// What a plugin may send its servers. Its commands queue with the panel's,
// which the server answers one at a time, so a plugin has one waiting at
// most and sends a few a second: one that loops on server.say can't hold up
// the panel.
const (
	actionRate  = 4.0 // commands a second, past actionBurst at once
	actionBurst = 8.0

	actionQueue = 64               // commands waiting to be sent
	actionTime  = 15 * time.Second // the longest one waits for its turn and the reply
	maxChat     = 200              // bytes of chat, the server's multiplayer_chat_max_bytes
	maxConsole  = 1024             // bytes of a console line, as the panel's console sends
	maxMovers   = 64               // players one teleport moves, the server's max_teleport_movers
	maxCoord    = 100000           // the server's max_teleport_coordinate
	maxMinutes  = 1440             // the longest kick, ReSkate 2.0.8
)

// action is a console line a plugin's call sends, later, on its own
// goroutine (runner.send).
type action struct {
	gen    uint64 // the plugin's Lua that asked: one that started afresh since gets no answer
	server string
	in     *instance.Instance
	call   string // "server.kick", for its log
	line   string
	audit  string // what the audit log calls it
	detail string
	fn     luavm.Ref // called with what came of it, when hasFn
	hasFn  bool
	// roles, for server.makerole, changes data/roles.json in place of line;
	// it says what it did, and what the audit log calls it.
	roles func(*serverconfig.File) (reply, audit string, err error)
	tell  string // for reskate.send, the ReSkate plugin line is the message to
}

// result is what came of an action, back on the runner's goroutine.
type result struct {
	action
	reply string
	err   error
}

// serverCall answers server.*: reads at once, actions queued.
func (r *runner) serverCall(name string, args []any) ([]any, error) {
	what := strings.TrimPrefix(name, "server.")
	if what == "list" {
		return []any{r.infos()}, nil
	}
	// An action's callback is its last argument, whatever comes before it.
	var a action
	if n := len(args); n > 0 {
		if fn, ok := args[n-1].(luavm.Ref); ok && n > 1 {
			a.fn, a.hasFn = fn, true
			args = args[:n-1]
		}
	}
	arg := func(i int) any {
		if i < len(args) {
			return args[i]
		}
		return nil
	}
	in, err := r.server(name, 1, arg(0))
	if err != nil {
		return nil, err
	}
	switch what {
	case "info":
		return []any{info(in)}, nil
	case "players":
		return []any{players(in)}, nil
	case "roles":
		f, err := serverconfig.Read(in.Def().ConfigPath())
		switch {
		case err != nil:
			return []any{nil, err.Error()}, nil
		case !f.HasRoles():
			return []any{nil, serverconfig.ErrNoRoles.Error()}, nil
		}
		list, _, _ := f.Roles()
		out := make([]any, len(list))
		for i, role := range list {
			out[i] = map[string]any{"name": role.Name, "tag": role.Tag, "color": role.Color, "rank": role.Rank}
		}
		return []any{out}, nil
	}
	a.server, a.in, a.call = in.ID(), in, name
	var perm string
	switch what {
	case "say":
		text, err := chat(name, 2, arg(1))
		if err != nil {
			return nil, err
		}
		perm, a.line, a.audit, a.detail = auth.PlayersChat, "say "+text, "player.say", text
	case "msg":
		id, err := steamID(name, 2, arg(1))
		if err != nil {
			return nil, err
		}
		text, err := chat(name, 3, arg(2))
		if err != nil {
			return nil, err
		}
		perm, a.line, a.audit, a.detail = auth.PlayersChat, "msg "+id+" "+text, "player.msg", id+" "+text
	case "kick":
		id, err := steamID(name, 2, arg(1))
		if err != nil {
			return nil, err
		}
		perm, a.line, a.audit, a.detail = auth.PlayersKick, "kick "+id, "player.kick", id
		if v := arg(2); v != nil {
			m, ok := number(v)
			if !ok || m < 0 || m > maxMinutes || m != math.Trunc(m) {
				return nil, fmt.Errorf("bad argument #3 to '%s' (minutes from 1 to %d, or 0 for until the server restarts)", name, maxMinutes)
			}
			// Before 2.0.8 the time is taken for part of the player's name,
			// and the kick lasts until the server restarts. A server that ran
			// has written every setting it knows.
			if f, err := serverconfig.Read(in.Def().ConfigPath()); err == nil {
				if _, ok := f.Get("kick_minutes"); !ok {
					return nil, fmt.Errorf("%s can't give a kick on %s a time: its kicks last until it restarts, and ReSkate 2.0.8 gives them one", name, a.server)
				}
			}
			when := strconv.Itoa(int(m))
			if m == 0 {
				when = "restart"
			}
			a.line, a.detail = a.line+" "+when, a.detail+" "+when
		}
	case "ban", "unban":
		id, err := steamID(name, 2, arg(1))
		if err != nil {
			return nil, err
		}
		perm, a.line, a.audit, a.detail = auth.PlayersBan, what+" "+id, "player."+what, id
	case "role":
		id, err := steamID(name, 2, arg(1))
		if err != nil {
			return nil, err
		}
		role, ok := arg(2).(string)
		if !ok {
			return nil, badArg(3, name, "string", arg(2))
		}
		if role = strings.ToLower(role); role != "none" && !serverconfig.RoleNameRe.MatchString(role) {
			return nil, fmt.Errorf("bad argument #3 to '%s' (a role's name, or \"none\")", name)
		}
		perm, a.line, a.detail = auth.IngameAdmins, "role "+id+" "+role, id+" "+role
		a.audit = "role.give"
		if role == "none" {
			a.audit = "role.take"
		}
	case "makerole":
		look, given, err := roleLook(name, arg(1))
		if err != nil {
			return nil, err
		}
		// a.roles names the audit entry once it reads the roles; if it never
		// does, it still has one.
		perm, a.detail, a.audit = auth.IngameAdmins, look.Name, "role.edit"
		a.roles = func(f *serverconfig.File) (string, string, error) {
			list, _, _ := f.Roles()
			i := slices.IndexFunc(list, func(r serverconfig.Role) bool { return r.Name == look.Name })
			if i < 0 {
				return "Added the " + look.Name + " role.", "role.create", f.AddRole(look)
			}
			// What the call leaves out stays as the role has it.
			l := serverconfig.RoleLook{Tag: list[i].Tag, Color: list[i].Color, Rank: list[i].Rank}
			if given["tag"] {
				l.Tag = look.Tag
			}
			if given["color"] {
				l.Color = look.Color
			}
			if given["rank"] {
				l.Rank = look.Rank
			}
			return "Saved the " + look.Name + " role.", "role.edit", f.EditRole(look.Name, l)
		}
	case "teleport":
		movers, err := teleportWho(name, arg(1))
		if err != nil {
			return nil, err
		}
		where, err := teleportWhere(name, arg(2))
		if err != nil {
			return nil, err
		}
		perm, a.line, a.audit, a.detail = auth.ConsoleExec, "teleport "+movers+" "+where, "player.teleport", movers+" "+where
	case "command":
		line, ok := arg(1).(string)
		if !ok {
			return nil, badArg(2, name, "string", arg(1))
		}
		line = strings.TrimSpace(line)
		switch err := supervisor.CheckLine(line); {
		case line == "":
			return nil, fmt.Errorf("bad argument #2 to '%s' (an empty command)", name)
		case len(line) > maxConsole:
			return nil, fmt.Errorf("bad argument #2 to '%s' (a command can be at most %d bytes)", name, maxConsole)
		case err != nil:
			return nil, fmt.Errorf("bad argument #2 to '%s' (%v)", name, err)
		}
		if verb := auth.OwnersCommand(line); verb != "" {
			return nil, fmt.Errorf("%s can't run %s: only owners can", name, verb)
		}
		// As in the panel's console: console.exec, and what the line does.
		if err := r.need(name, auth.ConsoleExec, a.server); err != nil {
			return nil, err
		}
		perm, a.line, a.audit, a.detail = auth.ConsolePerm(line), line, "console.exec", auth.AuditLine(line)
	default:
		return nil, fmt.Errorf("no function %s", name)
	}
	if perm != "" {
		if err := r.need(name, perm, a.server); err != nil {
			return nil, err
		}
	}
	a.gen = r.gen
	select {
	case r.acts <- a:
	default:
		r.p.mu.Lock()
		r.p.stats.Unsent++
		r.p.mu.Unlock()
		return []any{false, fmt.Sprintf("%d commands are waiting to be sent already", actionQueue)}, nil
	}
	if a.hasFn {
		r.vm.Keep(a.fn)
	}
	return []any{true}, nil
}

// need fails a call the plugin wasn't granted perm for.
func (r *runner) need(call, perm, server string) error {
	if r.m.perms(r.p.ID, r.man.Permissions).Can(perm, server) {
		return nil
	}
	how := "grant it on the plugin's card"
	if !slices.Contains(r.man.Permissions, perm) {
		how = fmt.Sprintf("add %q to permissions in its plugin.toml and reload it, then grant it on its card", perm)
	}
	return fmt.Errorf("%s needs %s (%s), which this plugin wasn't granted: %s", call, perm, grantLabel(perm), how)
}

// server is the server a call names as its argument n, if the plugin runs
// on it.
func (r *runner) server(call string, n int, v any) (*instance.Instance, error) {
	id, ok := v.(string)
	if !ok {
		return nil, badArg(n, call, "server ID", v)
	}
	if !r.onServer(id) {
		return nil, fmt.Errorf("bad argument #%d to '%s' (this plugin doesn't run on %q)", n, call, id)
	}
	if r.m.Servers != nil {
		if in, ok := r.m.Servers.Get(id); ok {
			return in, nil
		}
	}
	return nil, fmt.Errorf("bad argument #%d to '%s' (no server %q)", n, call, id)
}

// infos is server.info for each of the plugin's servers.
func (r *runner) infos() []any {
	out := []any{}
	if r.m.Servers == nil {
		return out
	}
	for _, in := range r.m.Servers.List() {
		if r.onServer(in.ID()) {
			out = append(out, info(in))
		}
	}
	return out
}

// info is a server as a plugin reads it. started_at is unix seconds.
func info(in *instance.Instance) map[string]any {
	v := in.View()
	m := map[string]any{"id": v.ID, "name": v.Name, "state": string(v.State), "players": v.Players}
	if v.Info.Map != "" {
		m["map"] = v.Info.Map
	}
	if v.Info.MaxPlayers > 0 {
		m["slots"] = v.Info.MaxPlayers
	}
	if v.StartedAt > 0 {
		m["started_at"] = v.StartedAt / 1000
	}
	return m
}

// players is who is on a server, first in first, as player.joined tells them.
func players(in *instance.Instance) []any {
	list := in.Players()
	slices.SortFunc(list, func(a, b instance.Player) int {
		return cmp.Or(cmp.Compare(a.JoinedAt, b.JoinedAt), cmp.Compare(a.ID, b.ID))
	})
	out := make([]any, len(list))
	for i, p := range list {
		m := map[string]any{"joined_at": p.JoinedAt / 1000}
		putPlayer(m, p)
		out[i] = m
	}
	return out
}

// chat is text for the server's chat: one line, cut at what it takes.
func chat(call string, n int, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", badArg(n, call, "string", v)
	}
	if s = supervisor.CutText(supervisor.CleanText(s), maxChat); s == "" {
		return "", fmt.Errorf("bad argument #%d to '%s' (no text to send)", n, call)
	}
	return s, nil
}

func steamID(call string, n int, v any) (string, error) {
	s, ok := v.(string)
	if !ok || !serverconfig.SteamIDRe.MatchString(s) {
		return "", badArg(n, call, "SteamID64", v)
	}
	return s, nil
}

// teleportWho is a SteamID64, or a list of up to maxMovers.
func teleportWho(call string, v any) (string, error) {
	if s, ok := v.(string); ok {
		return steamID(call, 2, s)
	}
	t, ok := v.(*luavm.Table)
	if !ok {
		return "", badArg(2, call, "SteamID64 or list", v)
	}
	var ids []string
	for _, e := range t.List() {
		id, err := steamID(call, 2, e)
		if err != nil {
			return "", fmt.Errorf("bad argument #2 to '%s' (a list of SteamID64s)", call)
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return "", fmt.Errorf("bad argument #2 to '%s' (nobody to teleport)", call)
	case len(ids) > maxMovers:
		return "", fmt.Errorf("bad argument #2 to '%s' (at most %d players a teleport)", call, maxMovers)
	}
	return strings.Join(ids, ","), nil
}

// teleportWhere is a player's SteamID64, or a place as {x =, y =, z =}
// (y is up), as a throwdown's near is.
func teleportWhere(call string, v any) (string, error) {
	if s, ok := v.(string); ok {
		return steamID(call, 3, s)
	}
	t, ok := v.(*luavm.Table)
	if !ok {
		return "", badArg(3, call, "SteamID64 or place", v)
	}
	var at []string
	for _, k := range []string{"x", "y", "z"} {
		f, ok := number(t.Get(k))
		if !ok || math.IsNaN(f) || math.Abs(f) > maxCoord {
			return "", fmt.Errorf("bad argument #3 to '%s' (a place is {x =, y =, z =}, each from -%d to %d)", call, maxCoord, maxCoord)
		}
		at = append(at, strconv.FormatFloat(f, 'f', -1, 32))
	}
	return strings.Join(at, " "), nil
}

// send sends the plugin's commands, one at a time and a few a second, until
// the runner stops: those from queue, rate a second past burst at once. Each
// is audited as the plugin's, and what came of it goes back to the runner.
func (r *runner) send(queue <-chan action, rate, burst float64) {
	tokens, at := burst, time.Now()
	for {
		var a action
		select {
		case <-r.quit:
			return
		case a = <-queue:
		}
		now := time.Now()
		tokens, at = min(burst, tokens+now.Sub(at).Seconds()*rate), now
		if tokens < 1 {
			select {
			case <-r.quit:
				return
			case <-time.After(time.Duration((1 - tokens) / rate * float64(time.Second))):
			}
			tokens, at = 1, time.Now()
		}
		tokens--
		ctx, cancel := context.WithTimeout(r.ctx, actionTime)
		var reply string
		var err error
		switch {
		case a.roles != nil:
			reply, err = a.in.ChangeRoles(ctx, "plugin:"+r.p.ID, func(f *serverconfig.File) (string, error) {
				reply, audit, err := a.roles(f)
				a.audit = audit
				return reply, err
			})
		case a.tell != "":
			reply, err = a.in.Tell(ctx, a.tell, a.line)
		default:
			reply, err = a.in.Command(ctx, a.line, "plugin:"+r.p.ID)
		}
		cancel()
		if !r.finish(a, reply, err) {
			return
		}
	}
}

// roleLook is server.makerole's {name =, tag =, color =, rank =}, and which
// of tag, color and rank it gave.
func roleLook(call string, v any) (serverconfig.RoleLook, map[string]bool, error) {
	var l serverconfig.RoleLook
	opts, err := options(call, 2, v, "name", "tag", "color", "rank")
	if err != nil {
		return l, nil, err
	}
	l.Name, _ = opts["name"].(string)
	if l.Name = strings.ToLower(l.Name); !serverconfig.RoleNameRe.MatchString(l.Name) || l.Name == "none" {
		return l, nil, fmt.Errorf("bad argument #2 to '%s' (name is 1 to 24 of a-z, 0-9, - and _, and not none)", call)
	}
	given := map[string]bool{}
	for _, k := range []string{"tag", "color"} {
		x, ok := opts[k]
		if !ok {
			continue
		}
		s, ok := x.(string)
		if !ok {
			return l, nil, fmt.Errorf("bad argument #2 to '%s' (%s is a string)", call, k)
		}
		if k == "tag" {
			l.Tag = s
		} else {
			l.Color = s
		}
		given[k] = true
	}
	if x, ok := opts["rank"]; ok {
		n, ok := number(x)
		if !ok || n != math.Trunc(n) || n < 0 || n > 1000 {
			return l, nil, fmt.Errorf("bad argument #2 to '%s' (rank is a whole number from 0 to 1000)", call)
		}
		l.Rank, given["rank"] = int(n), true
	}
	return l, given, nil
}

// finish counts and audits a command the plugin sent, then hands what came
// of it back; false once the runner has stopped.
func (r *runner) finish(a action, reply string, err error) bool {
	if err == nil && logparse.IsCommandError(reply) {
		err = errors.New(reply)
	}
	r.p.mu.Lock()
	r.p.stats.Sent++
	r.p.mu.Unlock()
	detail := a.detail
	if err != nil {
		detail += " (failed: " + err.Error() + ")"
	}
	if a.audit != "" { // "" for a message to a ReSkate plugin: data, not an act
		if err := r.m.Store.Audit(context.Background(), store.AuditEntry{Username: "plugin:" + r.p.ID, Instance: a.server, Action: a.audit, Detail: detail}); err != nil {
			r.m.Log.Warn("audit write failed", "err", err)
		}
	}
	return r.hand(a, reply, err)
}

// hand gives what came of an action back to the runner's goroutine; false
// once the runner has stopped.
func (r *runner) hand(a action, reply string, err error) bool {
	select {
	case <-r.quit:
		return false
	case r.jobs <- job{result: &result{action: a, reply: reply, err: err}}:
		return true
	}
}

// answered runs an action's callback with what came of it, or logs a
// failure nobody asked to hear of.
func (r *runner) answered(res *result) bool {
	if res.gen != r.gen || r.vm == nil { // its Lua started afresh since
		return true
	}
	if !res.hasFn {
		if res.err != nil && res.server == "" { // discord.post
			r.log("warn", fmt.Sprintf("%s: %v", res.call, res.err))
		} else if res.err != nil {
			r.log("warn", fmt.Sprintf("%s on %s: %v", res.call, res.server, res.err))
		}
		return true
	}
	vm := r.vm
	var ok bool
	if res.err != nil {
		ok = r.call(res.call+" callback", res.fn, false, res.err.Error())
	} else {
		ok = r.call(res.call+" callback", res.fn, true, res.reply)
	}
	if r.vm == vm && vm != nil {
		vm.Release(res.fn)
	}
	return ok || r.vm != nil
}
