package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xThrasherrr/ReSkateManager/internal/host"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// A mod zip is sent in chunks rather than one request, since a proxy in front
// of the panel can cap request bodies: Cloudflare turns away any over 100 MB.
const (
	maxModUpload  = 2 << 30
	maxModChunk   = 64 << 20
	modUploadIdle = time.Hour // an upload with no chunk for this long is dropped
	// Uploads open at once, for each user and in all. The panel sends one
	// at a time, so more are ones left behind, such as in a closed tab.
	uploadsPerUser = 4
	uploadsAll     = 16
	// uploadSpare is the space an upload must leave on its drive, beyond the
	// zip and about as much again for what it unpacks to.
	uploadSpare = 1 << 30
)

type modUpload struct {
	id     string
	target string // the instance it goes to, or sharedTarget
	user   int64
	name   string // the zip's file name
	path   string // where the chunks are written
	size   int64

	mu       sync.Mutex // one chunk at a time; guards the fields below
	received int64
	touched  time.Time
	gone     bool // installed or dropped
}

// sharedTarget is the target of uploads to the shared mods; no instance ID
// can be it.
const sharedTarget = "*shared"

// startModUpload opens an upload of a zip of the given size to a server's
// mods and answers its id and the chunk size to send it in.
func (a *API) startModUpload(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	a.startUpload(w, r, in.ID(), in.Def().Dir, maxModUpload)
}

// startUpload opens an upload of up to limit bytes to target, keeping its
// file in dir.
func (a *API) startUpload(w http.ResponseWriter, r *http.Request, target, dir string, limit int64) {
	var req struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Size <= 0 {
		writeErr(w, 400, "the file is empty")
		return
	}
	if req.Size > limit {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("files over %d GB cannot be uploaded", limit>>30))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.internalErr(w, r, err)
		return
	}
	if s, err := host.DiskSpace(dir); err == nil {
		if need := 2*uint64(req.Size) + uploadSpare; s.Free < need {
			writeErr(w, http.StatusInsufficientStorage, fmt.Sprintf("there isn't room for it: %s has %s free, and this needs about %s "+
				"(the zip, what it unpacks to, and 1 GB to spare)", s.Volume, gigabytes(s.Free), gigabytes(need)))
			return
		}
	}
	a.sweepModUploads()
	if err := a.roomForUpload(from(r).user.ID); err != nil {
		a.fail(w, r, http.StatusTooManyRequests, err)
		return
	}
	f, err := os.CreateTemp(dir, ".mod-upload-*.zip")
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	f.Close()
	u := &modUpload{id: rand.Text(), target: target, user: from(r).user.ID, name: req.Name, path: f.Name(), size: req.Size, touched: time.Now()}
	a.upMu.Lock()
	if a.modUploads == nil {
		a.modUploads = map[string]*modUpload{}
	}
	a.modUploads[u.id] = u
	a.upMu.Unlock()
	writeJSON(w, 200, map[string]any{"id": u.id, "chunk": maxModChunk})
}

// gigabytes writes a size for people, as "12.3 GB".
func gigabytes(n uint64) string { return fmt.Sprintf("%.1f GB", float64(n)/(1<<30)) }

// roomForUpload makes room for one more upload by user, dropping their
// longest-idle one if it's been left a few minutes, or says why there's none.
func (a *API) roomForUpload(user int64) error {
	a.upMu.Lock()
	var mine []*modUpload
	all := 0
	for _, u := range a.modUploads {
		all++
		if u.user == user {
			mine = append(mine, u)
		}
	}
	a.upMu.Unlock()
	if len(mine) >= uploadsPerUser {
		var oldest *modUpload
		for _, u := range mine {
			if u.mu.TryLock() {
				if !u.gone && time.Since(u.touched) > 2*time.Minute && (oldest == nil || u.touched.Before(oldest.touched)) {
					oldest = u
				}
				u.mu.Unlock()
			}
		}
		if oldest == nil {
			return fmt.Errorf("you have %d uploads under way; let one finish first", len(mine))
		}
		oldest.mu.Lock()
		if !oldest.gone {
			a.dropModUpload(oldest)
		}
		oldest.mu.Unlock()
		all--
	}
	if all >= uploadsAll {
		return errors.New("the manager has too many uploads under way; try again in a few minutes")
	}
	return nil
}

// modUploadChunk takes a chunk of an upload to a server's mods and, once the
// whole zip is in, installs it as a job: unpacking a big map can take longer
// than a proxy lets one request run.
func (a *API) modUploadChunk(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	dir := in.Def().Dir
	audit := a.auditLater(r, in.ID(), "mod.upload")
	a.uploadChunk(w, r, in.ID(), func(zipPath, name string) {
		a.startJob(w, r, func(ctx context.Context, progress func(done, total int64)) (any, error) {
			defer os.Remove(zipPath)
			un, err := serverconfig.UnpackMod(dir, zipPath, name)
			if err != nil {
				return nil, err
			}
			defer un.Discard()
			a.modMu.Lock()
			folder, replaced, err := un.Install("")
			a.modMu.Unlock()
			if err != nil {
				return nil, err
			}
			audit(folder)
			return map[string]any{"folder": folder, "replaced": replaced}, nil
		})
	})
}

// uploadChunk writes the request body at ?offset= into an upload to target
// and, once the whole zip is in, hands it to install, which answers and from
// then on owns the file. A chunk may be sent again from where it started.
func (a *API) uploadChunk(w http.ResponseWriter, r *http.Request, target string, install func(zipPath, name string)) {
	a.upMu.Lock()
	u := a.modUploads[chi.URLParam(r, "uid")]
	a.upMu.Unlock()
	if u == nil || u.target != target || u.user != from(r).user.ID {
		writeErr(w, 404, "that upload has expired; start it again")
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.gone {
		writeErr(w, 404, "that upload has expired; start it again")
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 || offset > u.received {
		writeErr(w, 409, fmt.Sprintf("expected a chunk at %d", u.received))
		return
	}
	// A chunk over a slow link can outlast the server's 30 s read timeout.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Minute))
	n, err := u.write(http.MaxBytesReader(w, r.Body, min(maxModChunk, u.size-offset)), offset)
	u.received, u.touched = offset+n, time.Now()
	if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
		writeErr(w, http.StatusRequestEntityTooLarge, "that chunk runs past the end of the file")
		return
	} else if err != nil {
		a.fail(w, r, 400, fmt.Errorf("upload: %w", err))
		return
	}
	if u.received < u.size {
		writeJSON(w, 200, map[string]any{"received": u.received})
		return
	}

	zipPath := u.path
	u.path = "" // install's now, not the upload's to delete
	a.dropModUpload(u)
	install(zipPath, u.name)
}

// write puts body into the file at offset, replacing anything after it, and
// returns how much it wrote. A chunk cut off part way is undone.
func (u *modUpload) write(body io.Reader, offset int64) (int64, error) {
	f, err := os.OpenFile(u.path, os.O_WRONLY, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err := f.Truncate(offset); err != nil {
		return 0, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.Copy(f, body)
	if err != nil {
		_ = f.Truncate(offset)
		return 0, err
	}
	return n, f.Close()
}

// dropModUpload forgets u and deletes its file. The caller holds u.mu.
func (a *API) dropModUpload(u *modUpload) {
	u.gone = true
	a.upMu.Lock()
	delete(a.modUploads, u.id)
	a.upMu.Unlock()
	if u.path != "" {
		os.Remove(u.path)
	}
}

// sweepModUploads drops uploads nobody has added to for a while, with their
// files.
func (a *API) sweepModUploads() {
	cutoff := time.Now().Add(-modUploadIdle)
	a.upMu.Lock()
	all := make([]*modUpload, 0, len(a.modUploads))
	for _, u := range a.modUploads {
		all = append(all, u)
	}
	a.upMu.Unlock()
	for _, u := range all {
		if !u.mu.TryLock() { // a chunk is on its way
			continue
		}
		if !u.gone && u.touched.Before(cutoff) {
			a.dropModUpload(u)
		}
		u.mu.Unlock()
	}
}

// SweepLeftovers deletes what uploads, downloads, installs and imports left
// beside the servers' and the shared mods when the manager stopped part way.
// Only at the start: then nothing is using them. A mod's old copy, kept aside
// until its new one was in, is left where it is and logged, being the only
// copy there is.
func (a *API) SweepLeftovers() {
	dirs := []string{a.SharedDir}
	for _, in := range a.Reg.List() {
		dirs = append(dirs, in.Def().Dir)
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, kept := range serverconfig.SweepLeftovers(dir) {
			a.Log.Warn("a mod being replaced when the manager stopped is still set aside; move it back into Mods by hand to keep it", "folder", kept)
		}
	}
	if a.ServersDir != "" {
		imports, _ := filepath.Glob(filepath.Join(a.ServersDir, ".import-*.zip"))
		for _, p := range imports {
			os.Remove(p)
		}
	}
}

// RunSweeps drops abandoned uploads every few minutes until ctx ends, so
// their files don't wait for the next upload to be noticed.
func (a *API) RunSweeps(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepModUploads()
		}
	}
}
