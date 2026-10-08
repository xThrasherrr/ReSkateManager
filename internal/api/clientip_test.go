package api

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/config"
)

func TestClientIP(t *testing.T) {
	const (
		stranger  = "203.0.113.9"  // someone on the internet
		nginx     = "10.0.0.2"     // a proxy on the LAN
		cfEdge    = "172.70.40.11" // one of Cloudflare's servers
		tailscale = "100.101.1.2"
	)
	cases := []struct {
		name  string
		proxy string
		peer  string
		hdr   map[string][]string
		want  string
	}{
		{"direct", config.ProxyNone, stranger, nil, stranger},
		{"headers ignored without a proxy", config.ProxyNone, nginx, map[string][]string{"Cf-Connecting-Ip": {"1.1.1.1"}, "X-Forwarded-For": {"2.2.2.2"}}, nginx},
		{"cloudflare", config.ProxyCloudflare, cfEdge, map[string][]string{"Cf-Connecting-Ip": {"198.51.100.4"}, "X-Forwarded-For": {"6.6.6.6, 198.51.100.4"}}, "198.51.100.4"},
		{"cloudflare tunnel", config.ProxyCloudflare, "127.0.0.1", map[string][]string{"Cf-Connecting-Ip": {"198.51.100.4"}}, "198.51.100.4"},
		{"cloudflare ipv6", config.ProxyCloudflare, "2606:4700::6810:1", map[string][]string{"Cf-Connecting-Ip": {"2001:db8::7"}}, "2001:db8::7"},
		{"cloudflare header missing", config.ProxyCloudflare, cfEdge, map[string][]string{"X-Forwarded-For": {"198.51.100.4"}}, cfEdge},
		{"cloudflare header straight from a stranger", config.ProxyCloudflare, stranger, map[string][]string{"Cf-Connecting-Ip": {"198.51.100.4"}}, stranger},
		{"forged cf header behind another proxy", config.ProxyForwarded, nginx, map[string][]string{"Cf-Connecting-Ip": {"6.6.6.6"}, "X-Forwarded-For": {"198.51.100.4"}}, "198.51.100.4"},
		{"forwarded through tailscale", config.ProxyForwarded, tailscale, map[string][]string{"X-Forwarded-For": {"198.51.100.4"}}, "198.51.100.4"},
		{"forwarded header straight from a stranger", config.ProxyForwarded, stranger, map[string][]string{"X-Forwarded-For": {"198.51.100.4"}}, stranger},
		{"forwarded header from cloudflare's servers", config.ProxyForwarded, cfEdge, map[string][]string{"X-Forwarded-For": {"198.51.100.4"}}, cfEdge},
		{"forged xff entry skipped", config.ProxyForwarded, nginx, map[string][]string{"X-Forwarded-For": {"6.6.6.6, 198.51.100.4"}}, "198.51.100.4"},
		{"xff split across headers", config.ProxyForwarded, nginx, map[string][]string{"X-Forwarded-For": {"6.6.6.6", "198.51.100.4"}}, "198.51.100.4"},
		{"mapped address", config.ProxyForwarded, nginx, map[string][]string{"X-Forwarded-For": {"::ffff:198.51.100.4"}}, "198.51.100.4"},
		{"empty xff entry falls back", config.ProxyForwarded, nginx, map[string][]string{"X-Forwarded-For": {"6.6.6.6, "}}, nginx},
		{"not an address falls back", config.ProxyForwarded, nginx, map[string][]string{"X-Forwarded-For": {"admin"}}, nginx},
		{"no proxy headers", config.ProxyForwarded, nginx, nil, nginx},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &API{Cfg: config.Config{Proxy: c.proxy}, Log: slog.New(slog.DiscardHandler)}
			r := &http.Request{RemoteAddr: "[" + c.peer + "]:51234", Header: http.Header(c.hdr)}
			if got := a.clientIP(r); got != c.want {
				t.Errorf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
}
