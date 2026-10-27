package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
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
	if p := auth.ConsolePerm(line); p != "" && !c.perms.Can(p, in.ID()) {
		writeErr(w, 403, "that command needs a permission you don't have: "+auth.PermLabel(p))
		return
	}
	if verb := auth.OwnersCommand(line); verb != "" && !c.perms.Owner {
		writeErr(w, 403, "only owners can run "+verb)
		return
	}
	reply, err := in.Command(r.Context(), line, c.user.Username)
	a.auditOutcome(r, in.ID(), "console.exec", auth.AuditLine(line), err)
	a.answerReply(w, r, reply, err)
}

// maxCommand is the longest console line the panel sends. The server's own
// longest, a list of score-allow fingerprints, is well under it.
const maxCommand = 1024
