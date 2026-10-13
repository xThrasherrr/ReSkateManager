package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A release's files can be built from a later commit than its tag: ReSkate
// 2.0.7's were uploaded again an hour after the tag, built from a hotfix
// (d2b8e18) that added settings and commands, and the tag stayed where it
// was. So the source is compared as each release was built.

// builtFrom is the commit a release's files were built from: the newest of
// the default branch to change Server/ before at, when the tag's commit is
// an ancestor of it, or else the tag itself. The branch is followed by its
// first parents, which is what it held at the time: a pull request's commits
// keep the date they were written, and are on it only from their merge
// (ReSkate#194's was written before 2.0.7's files were built, and merged a
// day after).
func builtFrom(src, tag string, at time.Time) (string, error) {
	head := "origin/HEAD" // the clone's default branch, whatever is checked out
	if _, err := git(src, "rev-parse", "--verify", "-q", head); err != nil {
		head = "HEAD"
	}
	out, err := git(src, "log", "-1", "--first-parent", "--format=%H", "--before="+at.UTC().Format(time.RFC3339), head, "--", "Server/")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(out)
	tagged, err := git(src, "rev-parse", tag+"^{commit}")
	if err != nil {
		return "", err
	}
	if commit == "" || commit == strings.TrimSpace(tagged) {
		return tag, nil
	}
	// Not an ancestor (git exits 1): the newest change before then is older
	// than the tag, which was built as it is.
	if _, err := git(src, "merge-base", "--is-ancestor", tag, commit); err != nil {
		return tag, nil
	}
	return commit, nil
}

// errNoRelease is a ref that isn't a release's tag: a commit, say, which is
// compared as it is.
var errNoRelease = errors.New("not a release")

// githubAPI is GitHub's API root, which tests replace.
var githubAPI = "https://api.github.com"

// uploaded is when a release's files were last uploaded.
func uploaded(ctx context.Context, repo, tag string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+"/repos/"+repo+"/releases/tags/"+tag, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("User-Agent", "ReSkateManager")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return time.Time{}, errNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("GitHub: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		Assets []struct {
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return time.Time{}, err
	}
	var last time.Time
	for _, a := range rel.Assets {
		if a.UpdatedAt.After(last) {
			last = a.UpdatedAt
		}
	}
	if last.IsZero() {
		return time.Time{}, errors.New("the release has no files")
	}
	return last, nil
}

// resolve finds the commits From and To were built from. was, when set, is
// what To was compared as when it was checked before (its report's built
// line): its files were uploaded again since, and From, To's tag too, is
// what was checked then.
func (r *report) resolve(ctx context.Context, was string) {
	r.toRef = r.built(ctx, r.To)
	if r.fromRef = was; was == "" {
		r.fromRef = r.built(ctx, r.From)
	}
}

// built is the commit a release's files were built from as they are now,
// or name itself when it isn't a release or that can't be told.
func (r *report) built(ctx context.Context, name string) string {
	at, err := uploaded(ctx, r.Repo, name)
	if err != nil {
		if !errors.Is(err, errNoRelease) {
			r.Notes = append(r.Notes, fmt.Sprintf("When %s's files were uploaded couldn't be read, so its tag was compared, not what they were built from: %s", name, code(err.Error())))
		}
		return name
	}
	commit, err := builtFrom(r.srcDir, name, at)
	if err != nil {
		r.Notes = append(r.Notes, fmt.Sprintf("What %s's files were built from couldn't be found, so its tag was compared: %s", name, code(err.Error())))
		return name
	}
	return commit
}

// sourceName names a release's source as compared: its tag, or the commit
// its files were built from.
func sourceName(name, ref string) string {
	if ref == "" || ref == name {
		return name
	}
	return fmt.Sprintf("%s as built (%s)", name, short(ref))
}

func short(ref string) string { return ref[:min(len(ref), 7)] }
