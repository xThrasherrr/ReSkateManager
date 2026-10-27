package auth

import (
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// What a console line needs, for the panel's console and for plugins'
// server.command alike: console.exec, and for most commands a finer
// permission too.

// consoleOnly are the console commands console.exec alone may run: they only
// read, or act on players in a way no finer permission covers. Of maps, only
// the list (see ConsolePerm). locks (2.1.0) lists the players with locked tools.
// Of plugin (ReSkate's plugins release), only list and commands.
var consoleOnly = []string{"help", "status", "net", "players", "bans", "maps", "tpall", "tphere", "teleport", "vote-cancel", "clear-objects", "locks", "plugin"}

// consolePerms are the console commands that do what a finer permission
// grants, and need it too. The verbs are the server's own (Host::command in
// Server/server_host.cpp), aliases included.
var consolePerms = func() map[string]string {
	// Roles (2.0.6) are who may run which commands in the game, and admins
	// are one of them; "roles reload" takes in an edit of the file.
	m := map[string]string{"admin": IngameAdmins, "admins": IngameAdmins, "role": IngameAdmins, "roles": IngameAdmins,
		"ban": PlayersBan, "unban": PlayersBan,
		// Taking a player's tools away (2.1.0) is a moderator's, as a kick is.
		"kick": PlayersKick, "lock": PlayersKick, "unlock": PlayersKick, "say": PlayersChat, "announce": PlayersChat, "announce-to": PlayersChat,
		"msg": PlayersChat, "msg-party": PlayersChat, "msg-admins": PlayersChat, "poll": PlayersChat,
		// The console starts votes too (2.0.3): they change what their
		// settings would, and a kick vote (ConsolePerm) kicks.
		"vote": SettingsEdit}
	for _, verb := range serverconfig.SettingVerbs {
		m[verb] = SettingsEdit
	}
	return m
}()

// ownerCommands are the console commands only owners may run: poll-run
// (2.0.3) runs any command for the winning answer, as custom votes do, and
// rsm speaks for the manager to the server's plugins (reskate.send).
var ownerCommands = []string{"poll-run", "rsm"}

// ConsolePerm is the permission a console line needs besides console.exec,
// "" for none. A command the manager doesn't know needs settings.edit, so one
// a newer server adds can't change settings for someone who may not.
func ConsolePerm(line string) string {
	fields := strings.Fields(strings.ToLower(line))
	if len(fields) == 0 {
		return ""
	}
	verb := fields[0]
	// "maps" lists the maps, but 2.0.7's maps reload reads Mods again, and
	// maps update and maps latest change the mods' versions and a setting.
	if verb == "maps" && len(fields) > 1 {
		return SettingsEdit
	}
	// plugin reload, off and on change what the server runs. A plugin's own
	// commands are ones the manager doesn't know, so they need settings.edit.
	if verb == "plugin" && len(fields) > 1 && fields[1] != "list" && fields[1] != "commands" {
		return SettingsEdit
	}
	if slices.Contains(consoleOnly, verb) {
		return ""
	}
	if verb == "vote" && len(fields) > 1 && fields[1] == "kick" {
		return PlayersKick
	}
	if p, ok := consolePerms[verb]; ok {
		return p
	}
	return SettingsEdit
}

// OwnersCommand is the line's command when only owners may run it, else "".
func OwnersCommand(line string) string {
	verb, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	if verb = strings.ToLower(verb); slices.Contains(ownerCommands, verb) {
		return verb
	}
	return ""
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
	return slices.Compact(out) // maps is in both
}

// PermLabel names a permission as the Users page does.
func PermLabel(key string) string {
	for _, p := range AllPerms {
		if p.Key == key {
			return p.Label
		}
	}
	return key
}

// AuditLine is a console line as the audit log keeps it, without a password.
func AuditLine(line string) string {
	if verb, arg, ok := strings.Cut(line, " "); ok && strings.EqualFold(verb, "password") && !strings.EqualFold(strings.TrimSpace(arg), "off") {
		return "password <hidden>"
	}
	return line
}
