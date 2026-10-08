// Package config is the manager's own settings file, data/manager.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is data/manager.toml, as the manager reads it.
type Config struct {
	// Listen is the panel's address. The default is every interface, so hosts
	// can reach the panel remotely; set "127.0.0.1:40125" to keep it local.
	Listen string `toml:"listen"`
	// PublicURL is the address people open the panel at, e.g.
	// "https://panel.example.com". Steam sign-in returns to it; without it,
	// Steam sign-in works only on the manager's own machine (localhost).
	PublicURL string `toml:"public_url"`
	TLSCert   string `toml:"tls_cert"`
	TLSKey    string `toml:"tls_key"`
	// OpenBrowser opens the panel when the manager starts on a desktop.
	OpenBrowser bool `toml:"open_browser"`
	// UpdateRepo is the GitHub repository whose releases carry the server.
	UpdateRepo string `toml:"update_repo"`
	// ManagerRepo is the GitHub repository whose releases are new versions of
	// the manager itself; empty turns the check off.
	ManagerRepo string `toml:"manager_repo"`
	// Proxy names the reverse proxy in front of the panel, which decides where
	// the visitor's address is read from: one of the Proxy* values.
	Proxy string `toml:"proxy"`
	// Unknown lists the keys the file has that the manager doesn't know,
	// such as a misspelt one, which Load can't use; never saved.
	Unknown []string `toml:"-"`
}

// The Proxy values: where the manager finds a visitor's address when it is
// reached through a proxy. With ProxyNone, the connection's own address.
const (
	ProxyNone       = ""
	ProxyCloudflare = "cloudflare" // Cf-Connecting-IP, which Cloudflare overwrites; proxy or Tunnel
	ProxyForwarded  = "forwarded"  // the last X-Forwarded-For entry: nginx, Caddy, Traefik, ...
)

// Default is the configuration a new manager starts with.
func Default() Config {
	return Config{
		Listen:      "0.0.0.0:40125",
		OpenBrowser: true,
		UpdateRepo:  "Dingo-Shenanigans/ReSkate",
		ManagerRepo: "xThrasherrr/ReSkateManager",
	}
}

const header = `# ReSkateManager settings. Restart the manager after editing.
#
# listen       panel address; "127.0.0.1:40125" keeps it to this machine
# public_url   the address people open the panel at, e.g. "https://panel.example.com"; Steam sign-in
#              needs it anywhere but on this machine. RSM_PUBLIC_URL overrides it
# tls_cert/key PEM files to serve the panel over HTTPS directly
# proxy        the reverse proxy in front of the panel, so sign-in limits and the audit log see
#              each visitor's own address: "cloudflare" (proxy or Tunnel), "forwarded" (nginx,
#              Caddy, Traefik, ...) or "" for none. Only the proxy may reach the panel's port, or
#              anyone can claim any address. RSM_PROXY overrides it.
# update_repo  GitHub repo whose releases carry the ReSkate server
# manager_repo GitHub repo whose releases are new versions of this manager ("" to stop checking)

`

// Load reads path, writing the defaults first when it does not exist. The
// RSM_PROXY and RSM_PUBLIC_URL environment variables override the file, so a
// container can be set up from compose.yaml alone.
func Load(path string) (Config, error) {
	c, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = Save(path, c)
	}
	if err != nil {
		return c, err
	}
	// After saving, so the environment never lands in the file.
	if v, ok := os.LookupEnv(envProxy); ok {
		c.Proxy = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := os.LookupEnv(envPublicURL); ok {
		c.PublicURL = v
	}
	if !ValidProxy(c.Proxy) {
		return c, fmt.Errorf("proxy %q: use %q, %q or \"\"", c.Proxy, ProxyCloudflare, ProxyForwarded)
	}
	pub, err := CleanPublicURL(c.PublicURL)
	if err != nil {
		return c, fmt.Errorf("public_url %q: %w", c.PublicURL, err)
	}
	c.PublicURL = pub
	if _, port, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("listen %q: use an address and a port, like \"0.0.0.0:40125\"", c.Listen)
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return c, fmt.Errorf("listen %q: the port must be 1 to 65535", c.Listen)
	}
	// One without the other would serve plain HTTP, the cookie with it.
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("tls_cert and tls_key go together: set both to serve HTTPS, or neither")
	}
	return c, nil
}

// read decodes the file alone, without the environment, over the defaults.
func read(path string) (Config, error) {
	c := Default()
	md, err := toml.DecodeFile(path, &c)
	for _, k := range md.Undecoded() {
		c.Unknown = append(c.Unknown, k.String())
	}
	c.Proxy = strings.ToLower(strings.TrimSpace(c.Proxy))
	return c, err
}

const (
	envProxy     = "RSM_PROXY"
	envPublicURL = "RSM_PUBLIC_URL"
)

// FromEnv reports which settings the environment sets, as RSM_PROXY and
// RSM_PUBLIC_URL; the file's values for those are not in effect.
func FromEnv() (proxy, publicURL bool) {
	_, proxy = os.LookupEnv(envProxy)
	_, publicURL = os.LookupEnv(envPublicURL)
	return proxy, publicURL
}

// Update changes the file at path, keeping its other settings as the file has
// them rather than as the environment overrides them.
func Update(path string, change func(*Config)) error {
	c, err := read(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	change(&c)
	return Save(path, c)
}

// ValidProxy reports whether p is one of the Proxy values.
func ValidProxy(p string) bool {
	return p == ProxyNone || p == ProxyCloudflare || p == ProxyForwarded
}

// CleanPublicURL checks a public address and returns it as scheme://host.
func CleanPublicURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", errors.New("enter the address as it appears in the browser, like https://panel.example.com")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// Save writes c to path through a temporary file, so a crash mid-write
// cannot leave a half-written settings file.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".manager-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(header)
	if err == nil {
		err = toml.NewEncoder(f).Encode(c)
	}
	if err == nil {
		err = f.Sync() // on disk before it replaces the old file
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644) // CreateTemp makes it private
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
