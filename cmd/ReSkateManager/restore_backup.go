package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/backup"
	"github.com/xThrasherrr/ReSkateManager/internal/config"
	"github.com/xThrasherrr/ReSkateManager/internal/lockfile"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// restoreBackup puts back the database, settings and servers a backup holds,
// for --restore, and exits. The manager must be stopped: a running one would
// go on with the old database open.
func restoreBackup(root, zip string) error {
	if root == "" {
		root = defaultRoot()
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if zip, err = filepath.Abs(zip); err != nil {
		return err
	}
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	lock, err := lockfile.Acquire(filepath.Join(dataDir, "manager.lock"))
	if errors.Is(err, lockfile.ErrLocked) {
		return errors.New("the manager is running with this folder; stop it first")
	}
	if err != nil {
		return err
	}
	defer lock.Release()
	// A manager running from this folder holds its database open, and listens
	// where its manager.toml says. With no database, as on a wiped install,
	// none can be, and the default port may well be another manager's.
	if _, err := os.Stat(filepath.Join(dataDir, "manager.db")); err == nil {
		if err := notRunning(filepath.Join(dataDir, "manager.toml")); err != nil {
			return err
		}
	}

	svc := &backup.Service{Dir: filepath.Join(root, "backups"), DataDir: dataDir, Root: root,
		SharedDir: filepath.Join(root, serverconfig.SharedName), Version: version}
	rep, err := svc.RestoreAll(context.Background(), zip)
	if rep.Safety != "" {
		fmt.Println("The database it replaced is backed up in backups/" + rep.Safety)
	}
	if err != nil {
		return err
	}
	fmt.Println("Restored the database and settings from " + filepath.Base(zip))
	if len(rep.Servers) > 0 {
		fmt.Println("Restored servers: " + strings.Join(rep.Servers, ", "))
	}
	if len(rep.Moved) > 0 {
		fmt.Println("Moved into this manager's servers folder: " + strings.Join(rep.Moved, ", "))
	}
	if len(rep.Shared) > 0 {
		fmt.Println("Restored shared mods: " + strings.Join(rep.Shared, ", "))
	}
	for _, n := range rep.Notes {
		fmt.Println("Note: " + n)
	}
	fmt.Println("Start the manager. Servers whose program is missing can be installed again from their Updates page.")
	return nil
}

// notRunning fails when something listens on the panel address in the
// manager.toml at cfgPath, such as the manager the restore would pull its
// database from under.
func notRunning(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err != nil {
		return nil // nothing says where it would listen
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("manager.toml: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("something is listening on %s; stop the manager first", cfg.Listen)
	}
	return ln.Close()
}
