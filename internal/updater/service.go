package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

// Job is one instance's update in progress (or its last result).
type Job struct {
	Phase    string `json:"phase"` // downloading | waiting | installing | done | failed
	Version  string `json:"version,omitempty"`
	Done     int64  `json:"done,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Error    string `json:"error,omitempty"`
	By       string `json:"by,omitempty"`
	Started  int64  `json:"started"`
	Finished int64  `json:"finished,omitempty"`
}

// InstanceStatus is an instance's server release next to the newest one, and
// any update of it under way.
type InstanceStatus struct {
	Supported bool     `json:"supported"`
	Latest    *Release `json:"latest,omitempty"`
	LatestErr string   `json:"latestError,omitempty"`
	ExeSHA256 string   `json:"exeSha256,omitempty"`
	Installed bool     `json:"installed"`
	// Version is the release the installed server is from; empty when it
	// isn't one this manager knows, such as a build made from source.
	Version  string `json:"version,omitempty"`
	UpToDate bool   `json:"upToDate"`
	Job      *Job   `json:"job,omitempty"`
}

// Service installs server releases, on demand and, in Run, on its own.
type Service struct {
	Checker  *Checker
	Builds   *Builds
	Reg      *instance.Registry
	CacheDir string
	Log      *slog.Logger
	// Base ends when the manager shuts down, and with it the updates under
	// way; nil never ends. Wait waits for them.
	Base context.Context
	// OnInstalled records a finished install (for the audit log).
	OnInstalled func(instanceID, version, by string)
	// OnFailed hears of an update that failed; version is "" when it failed
	// before finding the release.
	OnFailed func(instanceID, version, by string, err error)
	// BeforeInstall backs up an installed server before its update stops it.
	// An error fails the update, leaving the server as it was.
	BeforeInstall func(in *instance.Instance) error

	mu   sync.Mutex
	jobs map[string]*Job
	work sync.WaitGroup
}

// Wait waits up to timeout for the updates under way to stop, after Base
// ended, and reports whether they did.
func (s *Service) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.work.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *Service) job(id string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		c := *j
		return &c
	}
	return nil
}

func (s *Service) setJob(id string, fn func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]*Job{}
	}
	j := s.jobs[id]
	if j == nil {
		j = &Job{}
		s.jobs[id] = j
	}
	fn(j)
}

// Status compares an instance's server with the newest release; force looks
// the release up again rather than reading what was found minutes ago.
func (s *Service) Status(ctx context.Context, in *instance.Instance, force bool) InstanceStatus {
	st := InstanceStatus{Supported: Supported(), Job: s.job(in.ID())}
	sum, _ := FileSHA256(in.Def().Exe())
	st.ExeSHA256, st.Installed = sum, sum != ""
	st.Version = s.Builds.Named(sum)
	if !st.Supported {
		st.LatestErr = "releases carry no server for this platform; update this server by replacing its files"
		return st
	}
	rel, err := s.Checker.Latest(ctx, force)
	if err != nil {
		st.LatestErr = err.Error()
		return st
	}
	st.Latest = rel
	st.UpToDate = sum == rel.ExeSHA256
	return st
}

// ErrRunning refuses an update of a server that has one under way.
var ErrRunning = errors.New("an update is already in progress")

// Start begins an update. whenEmpty waits for the server to have no players
// first; otherwise players are warned and the server restarts within a minute.
func (s *Service) Start(in *instance.Instance, whenEmpty bool, by string) error {
	if !Supported() {
		return errors.New("releases carry no server for this platform")
	}
	// Checked and taken at once, so two clicks (or a click and the automatic
	// update) can't both start one.
	s.mu.Lock()
	if j := s.jobs[in.ID()]; j != nil && j.Finished == 0 {
		s.mu.Unlock()
		return ErrRunning
	}
	if s.jobs == nil {
		s.jobs = map[string]*Job{}
	}
	s.jobs[in.ID()] = &Job{Phase: "downloading", By: by, Started: time.Now().UnixMilli()}
	s.mu.Unlock()
	base := s.Base
	if base == nil {
		base = context.Background()
	}
	s.work.Go(func() {
		err := s.run(base, in, whenEmpty)
		s.setJob(in.ID(), func(j *Job) {
			j.Finished = time.Now().UnixMilli()
			if err != nil {
				j.Phase, j.Error = "failed", err.Error()
			} else {
				j.Phase = "done"
			}
		})
		if err != nil {
			in.Note("Update failed: " + err.Error())
			s.Log.Warn("update failed", "instance", in.ID(), "err", err)
			if s.OnFailed != nil {
				var version string
				if j := s.job(in.ID()); j != nil {
					version = j.Version
				}
				s.OnFailed(in.ID(), version, by, err)
			}
		}
	})
	return nil
}

func (s *Service) run(base context.Context, in *instance.Instance, whenEmpty bool) error {
	ctx, cancel := context.WithTimeout(base, 2*time.Hour)
	defer cancel()
	rel, err := s.Checker.Latest(ctx, true)
	if err != nil {
		return err
	}
	s.setJob(in.ID(), func(j *Job) { j.Version = rel.Version })
	in.Note(fmt.Sprintf("Downloading server %s...", rel.Version))
	archive, err := s.Checker.Download(ctx, rel, s.CacheDir, func(done, total int64) {
		s.setJob(in.ID(), func(j *Job) { j.Done, j.Total = done, total })
	})
	if err != nil {
		return err
	}
	wasRunning := in.State() == instance.Running || in.State() == instance.Starting
	if wasRunning {
		if whenEmpty {
			s.setJob(in.ID(), func(j *Job) { j.Phase = "waiting" })
			in.Note("Update downloaded; it installs when nobody is on.")
			for len(in.Players()) > 0 {
				select {
				case <-ctx.Done():
					return errors.New("gave up waiting for the server to empty")
				case <-time.After(20 * time.Second):
				}
				if st := in.State(); st != instance.Running && st != instance.Starting {
					break
				}
			}
		} else if len(in.Players()) > 0 {
			s.setJob(in.ID(), func(j *Job) { j.Phase = "waiting" })
			_, _ = in.Command(ctx, "say The server restarts for an update in 60 seconds.", "manager")
			if !sleep(ctx, 50*time.Second) {
				return ctx.Err()
			}
			_, _ = in.Command(ctx, "say Restarting for an update in 10 seconds.", "manager")
			if !sleep(ctx, 10*time.Second) {
				return ctx.Err()
			}
		}
		// Someone may have stopped it while we waited; then leave it stopped.
		wasRunning = in.State() == instance.Running || in.State() == instance.Starting
	}
	if s.BeforeInstall != nil && in.View().Installed {
		if err := s.BeforeInstall(in); err != nil {
			return fmt.Errorf("could not back up the server first: %w", err)
		}
	}
	s.setJob(in.ID(), func(j *Job) { j.Phase = "installing" })
	stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
	err = in.Stop(stopCtx)
	stopCancel()
	if err != nil {
		in.Kill()
	}
	if err := in.BeginUpdate(); err != nil {
		return err
	}
	in.Note("Installing server " + rel.Version + "...")
	installed, err := Install(archive, in.Def().Dir)
	in.EndUpdate()
	if err != nil {
		return err
	}
	in.Note(fmt.Sprintf("Installed server %s (%d files).", rel.Version, len(installed)))
	if s.OnInstalled != nil {
		j := s.job(in.ID())
		by := ""
		if j != nil {
			by = j.By
		}
		s.OnInstalled(in.ID(), rel.Version, by)
	}
	if wasRunning {
		return in.Start()
	}
	return nil
}

// Run checks for releases every 30 minutes and updates instances that opted in, once they are empty.
func (s *Service) Run(ctx context.Context) {
	if !Supported() {
		return
	}
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		for _, in := range s.Reg.List() {
			if !in.Def().AutoUpdate {
				continue
			}
			st := s.Status(ctx, in, false)
			if st.Latest == nil || !st.Installed || st.UpToDate || (st.Job != nil && st.Job.Finished == 0) {
				continue
			}
			s.Log.Info("auto-updating", "instance", in.ID(), "version", st.Latest.Version)
			_ = s.Start(in, true, "auto-update")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sleep waits d, or less if ctx ends first, and reports whether it waited d.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
