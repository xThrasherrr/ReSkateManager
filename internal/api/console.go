package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

func (a *API) command(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		Line string `json:"line"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	line := strings.TrimSpace(req.Line)
	if line == "" {
		writeErr(w, 400, "empty command")
		return
	}
	if len(line) > maxCommand {
		writeErr(w, 400, fmt.Sprintf("a command can be at most %d bytes", maxCommand))
		return
	}
	c := from(r)
	if p := consolePerm(line); p != "" && !c.perms.Can(p, in.ID()) {
		writeErr(w, 403, "that command needs a permission you don't have: "+permLabel(p))
		return
	}
	if verb, _, _ := strings.Cut(line, " "); slices.Contains(ownerCommands, strings.ToLower(verb)) && !c.perms.Owner {
		writeErr(w, 403, "only owners can run "+strings.ToLower(verb)+": it runs a server command of its own")
		return
	}
	reply, err := in.Command(r.Context(), line, c.user.Username)
	a.auditOutcome(r, in.ID(), "console.exec", auditLine(line), err)
	a.answerReply(w, r, reply, err)
}

// maxCommand is the longest console line the panel sends. The server's own
// longest, a list of score-allow fingerprints, is well under it.
const maxCommand = 1024

// consoleOnly are the console commands console.exec alone may run: they only
// read, or act on players in a way no finer permission covers.
var consoleOnly = []string{"help", "status", "net", "players", "bans", "maps", "tpall", "tphere", "teleport", "vote-cancel", "clear-objects"}

// consolePerms are the console commands that do what a finer permission
// grants, and need it too. The verbs are the server's own (Host::command in
// Server/server_host.cpp), aliases included.
var consolePerms = func() map[string]string {
	// Roles (2.0.6) are who may run which commands in the game, and admins
	// are one of them; "roles reload" takes in an edit of the file.
	m := map[string]string{"admin": auth.IngameAdmins, "admins": auth.IngameAdmins, "role": auth.IngameAdmins, "roles": auth.IngameAdmins,
		"ban": auth.PlayersBan, "unban": auth.PlayersBan,
		"kick": auth.PlayersKick, "say": auth.PlayersChat, "announce": auth.PlayersChat, "announce-to": auth.PlayersChat,
		"msg": auth.PlayersChat, "msg-party": auth.PlayersChat, "msg-admins": auth.PlayersChat, "poll": auth.PlayersChat,
		// The console starts votes too (2.0.3): they change what their
		// settings would, and a kick vote (consolePerm) kicks.
		"vote": auth.SettingsEdit}
	for _, verb := range serverconfig.SettingVerbs {
		m[verb] = auth.SettingsEdit
	}
	return m
}()

// ownerCommands are the console commands only owners may run: poll-run
// (2.0.3) runs any command for the winning answer, as custom votes do.
var ownerCommands = []string{"poll-run"}

// consolePerm is the permission a console line needs besides console.exec,
// "" for none. A command the manager doesn't know needs settings.edit, so one
// a newer server adds can't change settings for someone who may not.
func consolePerm(line string) string {
	fields := strings.Fields(strings.ToLower(line))
	verb := fields[0]
	if slices.Contains(consoleOnly, verb) {
		return ""
	}
	if verb == "vote" && len(fields) > 1 && fields[1] == "kick" {
		return auth.PlayersKick
	}
	if p, ok := consolePerms[verb]; ok {
		return p
	}
	return auth.SettingsEdit
}

// ConsoleVerbs are the console commands the panel has decided on: those
// console.exec alone may run, those that need a finer permission too, and
// the owners'. One a newer server adds needs settings.edit until it's here,
// so cmd/reskate-watch reports each new command that isn't.
func ConsoleVerbs() []string {
	out := slices.Concat(consoleOnly, ownerCommands)
	for verb := range consolePerms {
		out = append(out, verb)
	}
	slices.Sort(out)
	return out
}

// permLabel names a permission as the Users page does.
func permLabel(key string) string {
	for _, p := range auth.AllPerms {
		if p.Key == key {
			return p.Label
		}
	}
	return key
}

// auditLine keeps a password out of the audit log.
func auditLine(line string) string {
	if verb, arg, ok := strings.Cut(line, " "); ok && strings.EqualFold(verb, "password") && !strings.EqualFold(strings.TrimSpace(arg), "off") {
		return "password <hidden>"
	}
	return line
}
