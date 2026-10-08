package instance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logfile"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

const (
	// ServerLog is the server's own log, which it writes next to itself.
	ServerLog = "ReSkateServer.log"
	// ConsoleLog is what the console showed while the server ran, written by
	// the manager: the server's lines and everything else the process printed,
	// such as Steam's, which never reach ServerLog, with the manager's notes
	// and the commands sent.
	ConsoleLog  = "console.log"
	historyRead = 512 << 10
	historyKeep = 500
)

// LogFile is one of a server's log files.
type LogFile struct {
	Name     string    `json:"name"`
	Path     string    `json:"-"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// LogFiles lists the copies of log name in server folder dir that exist,
// oldest first: name.3 to name.1, then name itself.
func LogFiles(dir, name string) []LogFile {
	var out []LogFile
	for i := logfile.Keep; i >= 0; i-- {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s.%d", name, i)
		}
		p := filepath.Join(dir, n)
		// Lstat: a link here, made by a mod or by hand, is not a log to hand out.
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			out = append(out, LogFile{Name: n, Path: p, Size: fi.Size(), Modified: fi.ModTime()})
		}
	}
	return out
}

// OpenLog opens a log file LogFiles listed in server folder dir. It opens it
// within dir, so a link put there since can't lead out of the folder.
func OpenLog(dir string, lf LogFile) (*os.File, error) {
	return os.OpenInRoot(dir, lf.Name)
}

// loadHistory seeds the console with the end of the server's own log, so the
// console is not blank after the manager restarts.
func (in *Instance) loadHistory() {
	f, err := os.Open(filepath.Join(in.def.Dir, ServerLog))
	if err != nil {
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > historyRead {
		f.Seek(st.Size()-historyRead, io.SeekStart)
	}
	entries := logparse.ReadLog(f, historyKeep)
	if len(entries) == 0 {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, e := range entries {
		in.appendLocked(e)
	}
	in.managerLineLocked("The lines above are from " + ServerLog + ", from before the manager started.")
}

// rotateLog moves a large ReSkateServer.log aside before the server starts and
// appends to it again, keeping the last few. The server holds it open while it
// runs, so it can't rotate as it goes the way console.log does.
func rotateLog(dir string) (string, error) {
	size, err := logfile.Rotate(filepath.Join(dir, ServerLog))
	if err != nil || size == 0 {
		return "", err
	}
	return fmt.Sprintf("Moved %s (%d MB) to %s.1.", ServerLog, size>>20, ServerLog), nil
}
