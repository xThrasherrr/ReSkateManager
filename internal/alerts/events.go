package alerts

import (
	"fmt"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

// ForCrash describes a server named name stopping when nobody asked it to.
// link is its panel page, or "".
func ForCrash(name, link string, c instance.Crash) Alert {
	a := Alert{Kind: Stopped, Title: name + " stopped", Line: c.Reason, Link: link}
	switch {
	case c.NoStart:
		a.Title = name + " could not restart"
		a.Text = "A restart after a crash failed, so it stays down until someone starts it."
	case c.GaveUp:
		a.Kind, a.Title = GaveUp, name+" keeps crashing"
		a.Text = fmt.Sprintf("It crashed again after %d restarts in a row, so it stays down until someone starts it.", c.Of)
	case c.Fatal:
		a.Text = "It hit a problem a restart can't fix, so it stays down until someone fixes it and starts it."
	case c.Retry > 0:
		a.Kind, a.Title = Crash, name+" crashed"
		a.Text = fmt.Sprintf("Exit code %d. Restarting in %s (try %d of %d).", c.Code, c.Retry, c.Attempt, c.Of)
	default:
		a.Text = fmt.Sprintf("It exited with code %d, and restarting after a crash is off for it.", c.Code)
	}
	return a
}

// ForRestartFailed describes a scheduled restart that failed.
func ForRestartFailed(name, link string, err error) Alert {
	return Alert{Kind: Stopped, Title: name + "'s scheduled restart failed", Text: "Check that it came back up.", Line: err.Error(), Link: link}
}

// ForBackupFailed describes a scheduled backup that failed.
func ForBackupFailed(link string, err error) Alert {
	return Alert{Kind: BackupFailed, Title: "A scheduled backup failed", Text: "The manager tries again in an hour.", Line: err.Error(), Link: link}
}

// ForUpdate describes a server update that was installed, or failed when err
// is set. by is who started it: a username, or "auto-update".
func ForUpdate(name, link, version, by string, err error) Alert {
	if err != nil {
		title := name + "'s update failed"
		if version != "" {
			title = name + "'s update to " + version + " failed"
		}
		return Alert{Kind: UpdateFailed, Title: title, Text: "The server keeps its current version.", Line: err.Error(), Link: link}
	}
	text := "Installed by " + by + "."
	if by == "auto-update" {
		text = "Installed by auto-update, once nobody was on."
	}
	return Alert{Kind: Updated, Title: name + " updated to " + version, Text: text, Link: link}
}

// ForManagerUpdate describes the manager starting on a version it had not run
// before.
func ForManagerUpdate(from, to, link string) Alert {
	return Alert{Kind: ManagerUpdated, Title: "ReSkateManager updated to " + to, Text: "It was on " + from + " before.", Link: link}
}
