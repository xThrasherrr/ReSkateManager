package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLoadProxy(t *testing.T) {
	cases := []struct {
		name, file, env string
		want            string
		wantErr         bool
	}{
		{"none", "", "", ProxyNone, false},
		{"file", `proxy = "cloudflare"`, "", ProxyCloudflare, false},
		{"env wins", `proxy = "forwarded"`, "Cloudflare", ProxyCloudflare, false},
		{"unknown", `proxy = "nginx"`, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manager.toml")
			if err := os.WriteFile(path, []byte(c.file), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.env != "" {
				t.Setenv("RSM_PROXY", c.env)
			}
			got, err := Load(path)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Load = %q, want an error", got.Proxy)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Proxy != c.want {
				t.Errorf("Proxy = %q, want %q", got.Proxy, c.want)
			}
		})
	}
}

func TestLoadEnvStaysOutOfFile(t *testing.T) {
	t.Setenv("RSM_PROXY", "cloudflare")
	t.Setenv("RSM_PUBLIC_URL", "https://panel.example.com")
	path := filepath.Join(t.TempDir(), "manager.toml")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Proxy != ProxyCloudflare || c.PublicURL != "https://panel.example.com" {
		t.Errorf("Load = %q, %q; want the environment's values", c.Proxy, c.PublicURL)
	}
	var saved Config
	if _, err := toml.DecodeFile(path, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Proxy != "" || saved.PublicURL != "" {
		t.Errorf("defaults file holds the environment: proxy %q, public_url %q", saved.Proxy, saved.PublicURL)
	}
}

func TestUpdateKeepsFileValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.toml")
	if err := os.WriteFile(path, []byte("listen = \"127.0.0.1:40125\"\nproxy = \"forwarded\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RSM_PUBLIC_URL", "https://from-env.example.com")
	if err := Update(path, func(c *Config) { c.Proxy = ProxyNone }); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Proxy != ProxyNone || c.Listen != "127.0.0.1:40125" {
		t.Errorf("Load = proxy %q, listen %q", c.Proxy, c.Listen)
	}
	var saved Config
	if _, err := toml.DecodeFile(path, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.PublicURL != "" {
		t.Errorf("the environment's public_url was written to the file: %q", saved.PublicURL)
	}
}

func TestCleanPublicURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "",
		" https://Panel.Example.com/ ":  "https://panel.example.com",
		"http://192.168.1.20:40125":     "http://192.168.1.20:40125",
		"panel.example.com":             "!",
		"ftp://panel.example.com":       "!",
		"https://panel.example.com/x":   "!",
		"https://u:p@panel.example.com": "!",
	} {
		got, err := CleanPublicURL(in)
		if (err != nil) != (want == "!") || (err == nil && got != want) {
			t.Errorf("CleanPublicURL(%q) = %q, %v", in, got, err)
		}
	}
}

// A setting that can't work stops the start, rather than doing something
// else quietly: plain HTTP for half a TLS pair, say.
func TestLoadChecks(t *testing.T) {
	for body, ok := range map[string]bool{
		`listen = "127.0.0.1:40125"`:                true,
		`listen = "[::]:40125"`:                     true,
		`listen = "40125"`:                          false,
		`listen = "0.0.0.0:99999"`:                  false,
		`tls_cert = "a.pem"`:                        false,
		"tls_cert = \"a.pem\"\ntls_key = \"b.pem\"": true,
		`public_url = "panel.example.com"`:          false,
		`public_url = "https://Panel.Example.com/"`: true,
		`proxy = "nginx"`:                           false,
	} {
		path := filepath.Join(t.TempDir(), "manager.toml")
		if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); (err == nil) != ok {
			t.Errorf("%q: %v", body, err)
		}
	}
	path := filepath.Join(t.TempDir(), "manager.toml")
	os.WriteFile(path, []byte("publc_url = \"https://x.example\"\n"), 0o600)
	c, err := Load(path)
	if err != nil || len(c.Unknown) != 1 || c.Unknown[0] != "publc_url" {
		t.Errorf("a misspelt key: %v %v", c.Unknown, err)
	}
	// trust_proxy went at 1.0, once every 0.9 had written proxy in its place.
	os.WriteFile(path, []byte("trust_proxy = true\n"), 0o600)
	if c, err = Load(path); err != nil || c.Proxy != ProxyNone || len(c.Unknown) != 1 || c.Unknown[0] != "trust_proxy" {
		t.Errorf("trust_proxy: proxy %q, unknown %v, %v", c.Proxy, c.Unknown, err)
	}
}
