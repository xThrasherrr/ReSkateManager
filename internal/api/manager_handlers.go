package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/alerts"
	"github.com/xThrasherrr/ReSkateManager/internal/config"
	"github.com/xThrasherrr/ReSkateManager/internal/housekeep"
)

// accessSettings is how people reach the panel: what the last setup step and
// the Manager settings page edit. Changes apply at once, without a restart.
type accessSettings struct {
	Proxy     string   `json:"proxy"`
	PublicURL string   `json:"publicUrl"`
	Locked    []string `json:"locked"`    // set by environment variables: "proxy", "publicUrl"
	Suggested string   `json:"suggested"` // the proxy this request looks like it came through
}

func (a *API) access(r *http.Request) accessSettings {
	c := a.conf()
	out := accessSettings{Proxy: c.Proxy, PublicURL: c.PublicURL, Locked: []string{}, Suggested: guessProxy(r)}
	envProxy, envPub := config.FromEnv()
	if envProxy {
		out.Locked = append(out.Locked, "proxy")
	}
	if envPub {
		out.Locked = append(out.Locked, "publicUrl")
	}
	return out
}

// guessProxy names the proxy a request seems to have come through. It only
// pre-selects an answer for the owner to confirm: anyone can send these
// headers by hand, so they decide nothing on their own.
func guessProxy(r *http.Request) string {
	switch {
	case r.Header.Get("Cf-Connecting-IP") != "":
		return config.ProxyCloudflare
	case r.Header.Get("X-Forwarded-For") != "":
		return config.ProxyForwarded
	}
	return config.ProxyNone
}

func (a *API) managerSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.access(r))
}

func (a *API) saveManagerSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proxy     *string `json:"proxy"`
		PublicURL *string `json:"publicUrl"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.ConfigPath == "" {
		writeErr(w, http.StatusBadRequest, "this manager has no settings file to save to")
		return
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	next := a.conf()
	envProxy, envPub := config.FromEnv()
	if req.Proxy != nil {
		p := strings.ToLower(strings.TrimSpace(*req.Proxy))
		if !config.ValidProxy(p) {
			writeErr(w, http.StatusBadRequest, "unknown proxy "+p)
			return
		}
		if envProxy && p != next.Proxy {
			writeErr(w, http.StatusBadRequest, "RSM_PROXY sets this; change it where the manager's environment is set, such as compose.yaml")
			return
		}
		next.Proxy = p
	}
	if req.PublicURL != nil {
		u, err := config.CleanPublicURL(*req.PublicURL)
		if err != nil {
			a.fail(w, r, http.StatusBadRequest, err)
			return
		}
		if envPub && u != next.PublicURL {
			writeErr(w, http.StatusBadRequest, "RSM_PUBLIC_URL sets this; change it where the manager's environment is set, such as compose.yaml")
			return
		}
		next.PublicURL = u
	}
	err := config.Update(a.ConfigPath, func(c *config.Config) {
		if !envProxy {
			c.Proxy = next.Proxy
		}
		if !envPub {
			c.PublicURL = next.PublicURL
		}
	})
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	a.live.Store(&next)
	proxy := next.Proxy
	if proxy == config.ProxyNone {
		proxy = "none"
	}
	a.audit(r, "", "manager.settings", "proxy "+proxy+", address "+next.PublicURL)
	writeJSON(w, http.StatusOK, a.access(r))
}

// PanelLink is the address of a panel page, such as "/s/lobby", for links
// from outside the panel; "" while its public address is unset.
func (a *API) PanelLink(page string) string {
	if u := a.conf().PublicURL; u != "" {
		return strings.TrimRight(u, "/") + page
	}
	return ""
}

// alertSettings are the Discord alerts, as the Manager page edits them.
func (a *API) alertSettings(w http.ResponseWriter, r *http.Request) {
	set, err := alerts.Load(r.Context(), a.Store)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhook": set.Webhook, "off": set.Off, "kinds": alerts.Kinds,
		"diskGB": set.DiskGB, "memPct": set.MemPct, "memMinutes": set.MemMinutes})
}

func (a *API) saveAlertSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Webhook    *string   `json:"webhook"`
		Off        *[]string `json:"off"`
		DiskGB     *int      `json:"diskGB"`
		MemPct     *int      `json:"memPct"`
		MemMinutes *int      `json:"memMinutes"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	a.cfgMu.Lock() // so two saves at once can't each keep only their own change
	defer a.cfgMu.Unlock()
	set, err := alerts.Load(r.Context(), a.Store)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if req.Webhook != nil {
		set.Webhook = strings.TrimSpace(*req.Webhook)
	}
	if req.Off != nil {
		set.Off = *req.Off
	}
	for _, f := range []struct{ to, from *int }{{&set.DiskGB, req.DiskGB}, {&set.MemPct, req.MemPct}, {&set.MemMinutes, req.MemMinutes}} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
	if err := alerts.Save(r.Context(), a.Store, set); err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	detail := "no webhook"
	if set.Webhook != "" {
		detail = "webhook set"
	}
	if len(set.Off) > 0 {
		detail += ", off: " + strings.Join(set.Off, ", ")
	}
	detail += fmt.Sprintf("; disk under %d GB, memory at %d%% for %d min", set.DiskGB, set.MemPct, set.MemMinutes)
	a.audit(r, "", "manager.alerts", detail)
	a.alertSettings(w, r)
}

// testAlert posts a test alert to the webhook given, before it is saved, or
// else to the saved one, and answers Discord's error if it failed.
func (a *API) testAlert(w http.ResponseWriter, r *http.Request) {
	if a.Alerts == nil {
		writeErr(w, http.StatusNotFound, "alerts are not set up in this manager")
		return
	}
	var req struct {
		Webhook string `json:"webhook"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	hook := strings.TrimSpace(req.Webhook)
	if hook == "" {
		set, err := alerts.Load(r.Context(), a.Store)
		if err != nil {
			a.internalErr(w, r, err)
			return
		}
		hook = set.Webhook
	}
	if hook == "" {
		writeErr(w, http.StatusBadRequest, "enter a webhook address first")
		return
	}
	if err := alerts.CheckWebhook(hook); err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	err := a.Alerts.Post(ctx, hook, alerts.Alert{Kind: alerts.Test, Title: "Test alert from ReSkateManager",
		Text: "Crashes, failed updates, low disk space and the like will show up here.", Link: a.PanelLink("/manager")})
	a.auditOutcome(r, "", "manager.alerts.test", "", err)
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}

// retention is how long the audit log and player history are kept.
func (a *API) retention(w http.ResponseWriter, r *http.Request) {
	ret, err := housekeep.LoadRetention(r.Context(), a.Store)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ret)
}

func (a *API) saveRetention(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AuditDays  *int `json:"auditDays"`
		PlayerDays *int `json:"playerDays"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	a.cfgMu.Lock() // so two saves at once can't each keep only their own change
	defer a.cfgMu.Unlock()
	ret, err := housekeep.LoadRetention(r.Context(), a.Store)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	if req.AuditDays != nil {
		ret.AuditDays = *req.AuditDays
	}
	if req.PlayerDays != nil {
		ret.PlayerDays = *req.PlayerDays
	}
	if err := housekeep.SaveRetention(r.Context(), a.Store, ret); err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	a.audit(r, "", "manager.retention", fmt.Sprintf("audit log %d days, player history %d days", ret.AuditDays, ret.PlayerDays))
	writeJSON(w, http.StatusOK, ret)
}
