package auth

// Permission keys. Instance permissions can be granted on one instance or on
// all ("*"); global ones only count when granted on "*".
const (
	ConsoleView      = "console.view"
	ConsoleExec      = "console.exec"
	PlayersView      = "players.view"
	PlayersKick      = "players.kick"
	PlayersBan       = "players.ban"
	PlayersChat      = "players.chat"
	Announcements    = "announcements.manage"
	SettingsView     = "settings.view"
	SettingsEdit     = "settings.edit"
	ServerLifecycle  = "server.lifecycle"
	ServerUpdate     = "server.update"
	IngameAdmins     = "ingame.admins.manage"
	IngameAdmin      = "ingame.admin"
	InstancesManage  = "instances.manage"
	PanelUsersManage = "panel.users.manage"
	AuditView        = "audit.view"
	HostView         = "host.view"
)

// PermInfo describes a permission for the Users page: its key, what it
// lets someone do, and whether it counts only when granted on all servers.
type PermInfo struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Global bool   `json:"global"`
}

// AllPerms is every permission, in the order the Users page lists them.
var AllPerms = []PermInfo{
	{ConsoleView, "View the live console", false},
	{ConsoleExec, "Run console commands", false},
	{PlayersView, "See players and player history", false},
	{PlayersKick, "Kick players", false},
	{PlayersBan, "Ban and unban players", false},
	{PlayersChat, "Send server chat messages", false},
	{Announcements, "Manage timed chat announcements", false},
	{SettingsView, "View server settings", false},
	{SettingsEdit, "Change server settings", false},
	{ServerLifecycle, "Start, stop and restart the server", false},
	{ServerUpdate, "Install server updates", false},
	{IngameAdmins, "Manage in-game admins", false},
	{IngameAdmin, "Be an in-game admin (with their linked Steam account)", false},
	{InstancesManage, "Create, edit and remove servers", true},
	{PanelUsersManage, "Manage panel users and roles", true},
	{AuditView, "View the audit log", true},
	{HostView, "See the machine's performance and every server's share of it", true},
}

func isGlobal(key string) bool {
	for _, p := range AllPerms {
		if p.Key == key {
			return p.Global
		}
	}
	return false
}

func validPerm(key string) bool {
	for _, p := range AllPerms {
		if p.Key == key {
			return true
		}
	}
	return false
}

// Perms is what one user may do.
type Perms struct {
	Owner      bool                       `json:"owner"`
	Global     map[string]bool            `json:"global"`     // global perms, and instance perms granted on "*"
	ByInstance map[string]map[string]bool `json:"byInstance"` // instance id -> perms granted on just that instance
}

// Can reports whether the user may do perm on an instance, or with "" for
// instanceID, on all of them. A global permission counts only when granted
// on all servers.
func (p Perms) Can(perm, instanceID string) bool {
	if p.Owner {
		return true
	}
	if p.Global[perm] {
		return true
	}
	if isGlobal(perm) || instanceID == "" {
		return false
	}
	return p.ByInstance[instanceID][perm]
}

// add records keys granted on inst ("*" for all instances). Global permissions
// granted on a single instance count for nothing, and so do keys this manager
// doesn't know, such as one a newer version added to a restored database.
func (p *Perms) add(keys []string, inst string) {
	for _, k := range keys {
		switch {
		case !validPerm(k):
		case inst == "*":
			p.Global[k] = true
		case !isGlobal(k):
			if p.ByInstance[inst] == nil {
				p.ByInstance[inst] = map[string]bool{}
			}
			p.ByInstance[inst][k] = true
		}
	}
}

// Covers reports whether p holds everything q holds, so a user with p may hand
// out q or edit a user who has it.
func (p Perms) Covers(q Perms) bool {
	if p.Owner {
		return true
	}
	if q.Owner {
		return false
	}
	for k, ok := range q.Global {
		if ok && !p.Global[k] {
			return false
		}
	}
	for inst, set := range q.ByInstance {
		for k, ok := range set {
			if ok && !p.Can(k, inst) {
				return false
			}
		}
	}
	return true
}

// HoldsAll reports whether p holds every key on all instances, which editing a
// role needs because the role may be granted anywhere.
func (p Perms) HoldsAll(keys []string) bool {
	if p.Owner {
		return true
	}
	for _, k := range keys {
		if !p.Global[k] {
			return false
		}
	}
	return true
}

// CanAny reports whether the user holds perm on any instance (or globally).
func (p Perms) CanAny(perm string) bool {
	if p.Owner || p.Global[perm] {
		return true
	}
	for _, set := range p.ByInstance {
		if set[perm] {
			return true
		}
	}
	return false
}

var builtinRoles = []struct {
	name  string
	perms []string
}{
	{"Administrator", []string{ConsoleView, ConsoleExec, PlayersView, PlayersKick, PlayersBan, PlayersChat, Announcements,
		SettingsView, SettingsEdit, ServerLifecycle, ServerUpdate, IngameAdmins, AuditView, HostView}},
	{"Moderator", []string{ConsoleView, PlayersView, PlayersKick, PlayersBan, PlayersChat, SettingsView}},
	{"Viewer", []string{ConsoleView, PlayersView, SettingsView}},
}

// Sees reports whether the user holds any permission on an instance. Managing
// servers counts on every one: it may edit, export or remove each.
func (p Perms) Sees(instanceID string) bool {
	if p.Owner || p.Global[InstancesManage] || len(p.ByInstance[instanceID]) > 0 {
		return true
	}
	for k, ok := range p.Global {
		if ok && !isGlobal(k) {
			return true
		}
	}
	return false
}

// For lists the permission keys a user holds on one instance (global ones included).
func (p Perms) For(instanceID string) []string {
	out := []string{}
	for _, info := range AllPerms {
		if p.Can(info.Key, instanceID) {
			out = append(out, info.Key)
		}
	}
	return out
}
