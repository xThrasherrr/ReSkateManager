package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The check before a restore asks the address in manager.toml, and nothing
// without one: a wiped install has none, and the default port may be another
// manager's.
func TestNotRunning(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "manager.toml")
	if err := notRunning(cfg); err != nil {
		t.Errorf("no manager.toml: %v", err)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Error("the check wrote a manager.toml")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	os.WriteFile(cfg, []byte(`listen = "`+ln.Addr().String()+`"`+"\n"), 0o644)
	if err := notRunning(cfg); err == nil {
		t.Error("something listens there, yet the check passed")
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := notRunning(cfg); err != nil {
		t.Errorf("nothing listens on %s now: %v", addr, err)
	}
}
