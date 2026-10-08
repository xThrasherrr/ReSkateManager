// Package alerts posts what goes wrong with the servers, and their updates, to
// a Discord channel through a webhook, so failures reach someone while nobody
// is watching the panel.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// The kinds of alert, each of which can be turned off.
const (
	Crash          = "crash"          // a server crashed, and restarts
	GaveUp         = "gaveUp"         // it crashed too often in a row to restart again
	Stopped        = "stopped"        // it stopped and does not restart: auto-restart off, a config problem, a failed start
	Updated        = "updated"        // a server update was installed
	UpdateFailed   = "updateFailed"   // a server update failed
	ManagerUpdated = "managerUpdated" // the manager started on a new version
	BackupFailed   = "backupFailed"   // a scheduled backup failed
	DiskLow        = "diskLow"        // a drive the manager keeps things on is running out of space, or has room again
	MemoryHigh     = "memoryHigh"     // memory has stayed high, or is back down
	Test           = "test"           // the panel's test button; always sent
)

// Kinds lists the kinds that can be turned off, in the panel's order.
var Kinds = []string{Crash, GaveUp, Stopped, Updated, UpdateFailed, ManagerUpdated, BackupFailed, DiskLow, MemoryHigh}

// Settings is where alerts go and which are left out. Kinds are listed off
// rather than on, so a kind added later is sent until someone turns it off.
type Settings struct {
	Webhook string   `json:"webhook"`
	Off     []string `json:"off"`
	// DiskGB is how much free space, in GB, a drive is low under.
	DiskGB int `json:"diskGB"`
	// Memory is high once at least MemPct percent of it has been in use for
	// MemMinutes, and back down once it has been under that as long.
	MemPct     int `json:"memPct"`
	MemMinutes int `json:"memMinutes"`
}

// Defaults holds the thresholds until someone changes them.
var Defaults = Settings{DiskGB: 10, MemPct: 90, MemMinutes: 10}

const settingsKey = "alerts"

var webhookRe = regexp.MustCompile(`^https://(?:(?:ptb|canary)\.)?discord(?:app)?\.com/api/(?:v\d+/)?webhooks/\d+/[\w-]+$`)

// CheckWebhook accepts a Discord webhook address, or "" for none.
func CheckWebhook(u string) error {
	if u != "" && !webhookRe.MatchString(u) {
		return errors.New("that is not a Discord webhook address; copy it from the channel's Integrations > Webhooks")
	}
	return nil
}

// Load reads the settings; none saved yet is no webhook, every kind on and
// the default thresholds.
func Load(ctx context.Context, s *store.Store) (Settings, error) {
	out := Defaults
	out.Off = []string{}
	v, err := s.Get(ctx, settingsKey)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(v), &out)
	if out.Off == nil {
		out.Off = []string{}
	}
	return out, err
}

// Save checks and stores the settings.
func Save(ctx context.Context, s *store.Store, set Settings) error {
	if err := CheckWebhook(set.Webhook); err != nil {
		return err
	}
	for _, k := range set.Off {
		if !slices.Contains(Kinds, k) {
			return fmt.Errorf("unknown alert %q", k)
		}
	}
	switch {
	case set.DiskGB < 1 || set.DiskGB > 10000:
		return errors.New("alert on 1 to 10000 GB of free space")
	case set.MemPct < 50 || set.MemPct > 99:
		return errors.New("alert on 50% to 99% of memory in use")
	case set.MemMinutes < 1 || set.MemMinutes > 1440:
		return errors.New("memory must stay high for 1 to 1440 minutes")
	}
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return s.Set(ctx, settingsKey, string(b))
}

// Alert is one message for the channel.
type Alert struct {
	Kind  string
	Title string // "Lobby crashed"
	Text  string // what happened, in a sentence or two
	Line  string // the server's last line, shown as code
	Link  string // the panel page, when the panel's address is known
	Clear bool   // the problem an earlier alert of its kind told of is over
}

// Notifier sends alerts one at a time, in order, in the background.
type Notifier struct {
	Store   *store.Store
	Log     *slog.Logger
	Version string
	HTTP    *http.Client // nil: a client with a 15 s timeout

	queueOnce sync.Once
	queue     chan Alert
}

// Start begins sending until ctx ends. Alerts sent before it wait for it.
func (n *Notifier) Start(ctx context.Context) {
	queue := n.waiting()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case a := <-queue:
				n.deliver(ctx, a)
			}
		}
	}()
}

// waiting is the queue of alerts to deliver, made on first use.
func (n *Notifier) waiting() chan Alert {
	n.queueOnce.Do(func() { n.queue = make(chan Alert, 64) })
	return n.queue
}

// Send queues an alert, if a webhook is set and its kind is on. It never
// blocks: when Discord is down for long enough to fill the queue, the newest
// alerts are dropped and logged.
func (n *Notifier) Send(a Alert) {
	set, err := Load(context.Background(), n.Store)
	if err != nil {
		n.Log.Warn("alert settings", "err", err)
		return
	}
	if set.Webhook == "" || slices.Contains(set.Off, a.Kind) {
		return
	}
	select {
	case n.waiting() <- a:
	default:
		n.Log.Warn("alert dropped: too many waiting", "title", a.Title)
	}
}

// deliver posts an alert to the webhook set now, trying again a few times
// while Discord limits the rate or is unavailable.
func (n *Notifier) deliver(ctx context.Context, a Alert) {
	set, err := Load(ctx, n.Store)
	if err != nil || set.Webhook == "" {
		return
	}
	for try := 1; ; try++ {
		wait, err := n.post(ctx, set.Webhook, a)
		if err == nil {
			return
		}
		if wait == 0 || try == 4 {
			n.Log.Warn("alert not delivered", "title", a.Title, "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Post sends one alert to webhook straight away, for the panel's test button.
func (n *Notifier) Post(ctx context.Context, webhook string, a Alert) error {
	_, err := n.post(ctx, webhook, a)
	return err
}

// post sends a to webhook. A failure worth trying again says how long to wait.
func (n *Notifier) post(ctx context.Context, webhook string, a Alert) (retry time.Duration, err error) {
	body, err := json.Marshal(n.message(a))
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ReSkateManager/"+n.Version)
	client := n.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// A webhook answers itself; a redirect would take the alert, and the
	// token in its address, somewhere else.
	cl := *client
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Do(req)
	if err != nil {
		// Without the address, which holds the webhook's token and would
		// land in the manager's log.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return 5 * time.Second, fmt.Errorf("discord: %w", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode < 300:
		return 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		// Discord says how long in seconds, in the header and the body.
		var limit struct {
			RetryAfter float64 `json:"retry_after"`
		}
		wait := 5 * time.Second
		if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil {
			wait = time.Duration(s * float64(time.Second))
		} else if json.Unmarshal(msg, &limit) == nil && limit.RetryAfter > 0 {
			wait = time.Duration(limit.RetryAfter * float64(time.Second))
		}
		return min(max(wait, time.Second), time.Minute), errors.New("discord: rate limited")
	case resp.StatusCode >= 500:
		return 10 * time.Second, fmt.Errorf("discord: HTTP %d", resp.StatusCode)
	}
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(msg, &e) == nil && e.Message != "" {
		return 0, fmt.Errorf("discord: %s (HTTP %d)", e.Message, resp.StatusCode)
	}
	return 0, fmt.Errorf("discord: HTTP %d", resp.StatusCode)
}

var colors = map[string]int{
	Crash:          0xff5ca8,
	GaveUp:         0xff4f5c,
	Stopped:        0xff4f5c,
	UpdateFailed:   0xff4f5c,
	BackupFailed:   0xff4f5c,
	DiskLow:        0xffd23f,
	MemoryHigh:     0xffd23f,
	Updated:        0x7ee26b,
	ManagerUpdated: 0x7ee26b,
	Test:           0x4cc3ff,
}

// message is the webhook's JSON for a. Text from a server, such as a player's
// name in its last line, mentions nobody: allowed_mentions turns every ping off.
func (n *Notifier) message(a Alert) map[string]any {
	desc := a.Text
	if line := strings.TrimSpace(a.Line); line != "" {
		// Inside a code block nothing formats; a ``` in the line would end it.
		desc += "\n```\n" + strings.ReplaceAll(truncate(line, 900), "```", "ˋˋˋ") + "\n```"
	}
	embed := map[string]any{
		"title":       truncate(a.Title, 250),
		"description": truncate(desc, 4000),
		"color":       colors[a.Kind],
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"footer":      map[string]string{"text": "ReSkateManager " + n.Version},
	}
	if a.Clear {
		embed["color"] = colors[Updated]
	}
	if a.Link != "" {
		embed["url"] = a.Link
	}
	return map[string]any{
		"username":         "ReSkateManager",
		"embeds":           []any{embed},
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
