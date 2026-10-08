package api

import (
	"fmt"
	"net/http"

	"github.com/xThrasherrr/ReSkateManager/internal/announce"
	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// announcements lists this server's announcements and those for every server.
// allServers says whether the caller may manage the latter.
func (a *API) announcements(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.Announcements(r.Context(), inst(r).ID())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"announcements": list, "allServers": from(r).perms.Can(auth.Announcements, "")})
}

type announcementReq struct {
	AllServers bool   `json:"allServers"`
	Message    string `json:"message"`
	Interval   int    `json:"interval"` // seconds
	Enabled    bool   `json:"enabled"`
}

// fill validates req into an announcement for the server in the URL.
func (a *API) fill(w http.ResponseWriter, r *http.Request, req announcementReq, out *store.Announcement) bool {
	msg := cleanText(req.Message)
	if msg == "" || len(msg) > 200 {
		writeErr(w, 400, "messages are 1-200 bytes")
		return false
	}
	if !announce.ValidInterval(req.Interval) {
		writeErr(w, 400, "the interval is 1 minute to 7 days")
		return false
	}
	out.Instance = inst(r).ID()
	if req.AllServers {
		out.Instance = store.AllInstances
	}
	if !a.mayManage(w, r, out.Instance) {
		return false
	}
	out.Message, out.Interval, out.Enabled = msg, req.Interval, req.Enabled
	return true
}

// mayManage checks that the caller may change announcements scoped to
// instanceID; every server's need the permission on all servers.
func (a *API) mayManage(w http.ResponseWriter, r *http.Request, instanceID string) bool {
	if instanceID == store.AllInstances && !from(r).perms.Can(auth.Announcements, "") {
		writeErr(w, http.StatusForbidden, "announcements for every server need the permission on all servers")
		return false
	}
	return true
}

// loadAnnouncement fetches the announcement in the URL, if it applies to the
// server in the URL and the caller may change it.
func (a *API) loadAnnouncement(w http.ResponseWriter, r *http.Request) (store.Announcement, bool) {
	id, ok := int64Param(w, r, "aid", "announcement")
	if !ok {
		return store.Announcement{}, false
	}
	ann, err := a.Store.Announcement(r.Context(), id)
	if err == nil && ann.Instance != inst(r).ID() && ann.Instance != store.AllInstances {
		err = store.ErrNotFound
	}
	if err != nil {
		writeErr(w, errStatus(err), "no such announcement")
		return ann, false
	}
	return ann, a.mayManage(w, r, ann.Instance)
}

func auditScope(ann store.Announcement) string {
	if ann.Instance == store.AllInstances {
		return ""
	}
	return ann.Instance
}

// maxAnnouncements caps the announcements one server sends, its own and
// every server's together. The scheduler reads them all every few seconds.
const maxAnnouncements = 50

func (a *API) addAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req announcementReq
	if !readJSON(w, r, &req) {
		return
	}
	var ann store.Announcement
	if !a.fill(w, r, req, &ann) {
		return
	}
	// Every server's goes to each server, so each must have room for it.
	servers := []*instance.Instance{inst(r)}
	if ann.Instance == store.AllInstances {
		servers = a.Reg.List()
	}
	for _, in := range servers {
		list, err := a.Store.Announcements(r.Context(), in.ID())
		if err != nil {
			a.internalErr(w, r, err)
			return
		}
		if len(list) >= maxAnnouncements {
			writeErr(w, http.StatusConflict, fmt.Sprintf("%s has %d announcements, the most a server can have; remove one first", in.Def().Name, len(list)))
			return
		}
	}
	if err := a.Store.SaveAnnouncement(r.Context(), &ann); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.audit(r, auditScope(ann), "announcement.add", ann.Message)
	writeJSON(w, 200, ann)
}

func (a *API) updateAnnouncement(w http.ResponseWriter, r *http.Request) {
	ann, ok := a.loadAnnouncement(w, r)
	if !ok {
		return
	}
	var req announcementReq
	if !readJSON(w, r, &req) || !a.fill(w, r, req, &ann) {
		return
	}
	if err := a.Store.SaveAnnouncement(r.Context(), &ann); err != nil {
		a.fail(w, r, errStatus(err), err)
		return
	}
	a.audit(r, auditScope(ann), "announcement.edit", ann.Message)
	writeJSON(w, 200, ann)
}

func (a *API) deleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	ann, ok := a.loadAnnouncement(w, r)
	if !ok {
		return
	}
	if err := a.Store.DeleteAnnouncement(r.Context(), ann.ID); err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.audit(r, auditScope(ann), "announcement.remove", ann.Message)
	w.WriteHeader(http.StatusNoContent)
}
