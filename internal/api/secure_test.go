package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/config"
)

func TestSecureCookieOnlyOnPublicAddress(t *testing.T) {
	a := &API{Cfg: config.Config{PublicURL: "https://panel.example.com"}}
	for host, want := range map[string]bool{"panel.example.com": true, "192.168.1.20:40125": false, "localhost:40125": false} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		if got := a.secure(r); got != want {
			t.Errorf("secure(%s) = %v, want %v", host, got, want)
		}
	}
}

// HSTS goes out only on requests that came over HTTPS.
func TestHSTSOnlyOverHTTPS(t *testing.T) {
	a := &API{Cfg: config.Config{PublicURL: "https://panel.example.com"}}
	h := a.securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for host, want := range map[string]bool{"panel.example.com": true, "192.168.1.20:40125": false} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+"/", nil))
		if got := w.Header().Get("Strict-Transport-Security") != ""; got != want {
			t.Errorf("HSTS on %s = %v, want %v", host, got, want)
		}
	}
}
