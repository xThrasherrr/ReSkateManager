// Package discord keeps a live status message in a Discord channel: a bot
// posts one embed listing every server, then edits that same message as the
// servers change. It uses Discord's REST API only, so it holds no connection
// open and works from any install that can reach discord.com.
package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// Settings is the bot and the channel it posts in. The token stays in the
// manager: the panel only learns whether one is set.
type Settings struct {
	Token   string `json:"token"`
	Channel string `json:"channel"`
	// JoinCodes shows each running server's join code.
	JoinCodes bool `json:"joinCodes"`
	// Title heads the message; "" is DefaultTitle.
	Title string `json:"title"`
}

// DefaultTitle heads the message until the owner names it.
const DefaultTitle = "Server status"

// MaxTitle is the longest title Discord shows.
const MaxTitle = 256

// Defaults holds the settings until someone changes them.
var Defaults = Settings{JoinCodes: true}

const (
	settingsKey = "discord"
	postedKey   = "discord.message" // the message posted, and where
)

// DefaultAPI is Discord's REST API.
const DefaultAPI = "https://discord.com/api/v10"

// Permissions is what the bot needs in its channel: View Channel, Send
// Messages and Embed Links.
const Permissions = 1<<10 | 1<<11 | 1<<14

var (
	tokenRe     = regexp.MustCompile(`^[\w-]{10,}\.[\w-]{4,}\.[\w-]{20,}$`)
	snowflakeRe = regexp.MustCompile(`^\d{17,20}$`)
	// A link to the channel, or to a message in it, as Discord's Copy Link gives.
	channelLinkRe = regexp.MustCompile(`^https://(?:(?:ptb|canary)\.)?discord(?:app)?\.com/channels/\d+/(\d{17,20})(?:/\d+)?/?$`)
)

// CleanToken tidies a bot token as pasted, without spaces or a "Bot " in
// front, and refuses what can't be one, such as the application's client
// secret.
func CleanToken(s string) (string, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Bot "))
	if !tokenRe.MatchString(s) {
		return "", errors.New("that is not a bot token; copy it from the Bot page of your application in Discord's Developer Portal")
	}
	return s, nil
}

// CleanChannel takes a channel's ID, or a link to the channel or to a message
// in it, and returns the ID; "" stays "", for no channel.
func CleanChannel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if m := channelLinkRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if s != "" && !snowflakeRe.MatchString(s) {
		return "", errors.New("that is not a channel ID; in Discord, right-click the channel and choose Copy Channel ID (Developer Mode, under Advanced, shows it)")
	}
	return s, nil
}

// CleanTitle tidies a title for the message: one line, at most MaxTitle
// characters. "" stays "", for DefaultTitle.
func CleanTitle(s string) (string, error) {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) > MaxTitle {
		return "", fmt.Errorf("a title is at most %d characters", MaxTitle)
	}
	return s, nil
}

// BotID is the ID of the bot a token is for, or "" for none: the first part of
// a token, which a reset in the Developer Portal keeps. It's the bot's
// application's ID too.
func BotID(token string) string {
	first, _, _ := strings.Cut(token, ".")
	id, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(first, "="))
	if err != nil || !snowflakeRe.Match(id) {
		return ""
	}
	return string(id)
}

// Invite is the link that adds the token's bot to a Discord server with the
// permissions it needs, or "" for no token.
func Invite(token string) string {
	id := BotID(token)
	if id == "" {
		return ""
	}
	return fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%s&scope=bot&permissions=%d", id, Permissions)
}

// Load reads the settings; none saved yet is no bot, with the defaults.
func Load(ctx context.Context, s *store.Store) (Settings, error) {
	out := Defaults
	v, err := s.Get(ctx, settingsKey)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(v), &out)
	return out, err
}

// Save checks and stores the settings.
func Save(ctx context.Context, s *store.Store, set Settings) error {
	if set.Token != "" {
		if _, err := CleanToken(set.Token); err != nil {
			return err
		}
	}
	if _, err := CleanChannel(set.Channel); err != nil {
		return err
	}
	if _, err := CleanTitle(set.Title); err != nil {
		return err
	}
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return s.Set(ctx, settingsKey, string(b))
}

// posted is the status message, once there is one.
type posted struct {
	Channel string `json:"channel"`
	Message string `json:"message"`
	Guild   string `json:"guild"` // the Discord server the channel is in
	Name    string `json:"name"`  // the channel's name
}

// Status is how the message is doing, for the Manager page.
type Status struct {
	Channel string `json:"channel,omitempty"` // the channel's name
	Link    string `json:"link,omitempty"`    // the message in Discord
	Updated int64  `json:"updated,omitempty"` // when it was last posted or edited, unix ms
	Problem string `json:"problem,omitempty"` // why the last try failed
	// Stopped is set when trying again can't help until the settings change,
	// such as a refused token.
	Stopped bool `json:"stopped,omitempty"`
}

const (
	tick = 15 * time.Second
	// minGap keeps edits a minute apart however often the servers change.
	minGap = time.Minute
	// heartbeat edits the message this often when nothing changed, so its
	// "updated" time shows the manager still runs.
	heartbeat = 5 * time.Minute
)

// Poster keeps the status message up to date.
type Poster struct {
	Store   *store.Store
	Reg     *instance.Registry
	Log     *slog.Logger
	Version string
	// Release names the ReSkate release a server's program is from, or "".
	Release func(exe string) string
	HTTP    *http.Client // nil: a client with a 15 s timeout
	API     string       // "": DefaultAPI

	mu     sync.Mutex // guards status
	status Status

	// run holds one update at a time, and guards what follows.
	run     sync.Mutex
	sent    string    // the embed last sent, without its time
	tried   time.Time // when it was sent, or last failed to be
	until   time.Time // the end of a rate limit
	stopFor string    // the token and channel that were refused
	// later is the next try after Discord said no to something put right on
	// its side, such as the bot's access to the channel; refused counts those
	// in a row, for the backoff.
	later   time.Time
	refused int
}

// Run keeps the message up to date until ctx ends, then edits it once more to
// say the manager stopped: the bot stops with it, and can't say so later.
func (p *Poster) Run(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		p.update(ctx, time.Now(), false)
		select {
		case <-ctx.Done():
			p.farewell()
			return
		case <-t.C:
		}
	}
}

// Refresh brings the message up to date now, after a change of settings, and
// says how that went. A token or channel refused before is tried again.
func (p *Poster) Refresh(ctx context.Context) Status {
	p.update(ctx, time.Now(), true)
	return p.Status()
}

// Status is how the message is doing.
func (p *Poster) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *Poster) setStatus(s Status) {
	p.mu.Lock()
	p.status = s
	p.mu.Unlock()
}

// update edits the message when what it shows changed, and a minute has
// passed since the last edit, or when the heartbeat is due. force skips the
// wait, and tries a refused token or channel again.
func (p *Poster) update(ctx context.Context, now time.Time, force bool) {
	p.run.Lock()
	defer p.run.Unlock()
	set, err := Load(ctx, p.Store)
	if err != nil {
		if ctx.Err() == nil {
			p.Log.Warn("discord settings", "err", err)
		}
		return
	}
	if set.Token == "" || set.Channel == "" {
		p.sent, p.stopFor, p.refused = "", "", 0
		p.setStatus(Status{})
		return
	}
	key := set.Token + " " + set.Channel
	if force {
		p.stopFor, p.later, p.refused = "", time.Time{}, 0
	}
	if p.stopFor == key || now.Before(p.until) || now.Before(p.later) {
		return
	}
	e := p.render(set, false, now)
	body, _ := json.Marshal(e)
	if !force && (now.Sub(p.tried) < minGap || string(body) == p.sent && now.Sub(p.tried) < heartbeat) {
		return
	}
	p.tried = now
	e.Description += fmt.Sprintf("\n\nUpdated <t:%d:R>", now.Unix())
	pst, err := p.send(ctx, set, e)
	if err != nil && !errors.Is(err, errNoEmbed) {
		if ctx.Err() != nil {
			return
		}
		p.sent = ""      // try again in a minute, even with nothing changed
		st := p.Status() // still showing the last edit that went through
		was := st.Problem
		var r retry
		st.Problem, r = problem(err)
		st.Stopped = r == retryNever
		switch r {
		case retryNever:
			p.stopFor = key
		case retrySlow: // 1, 2, 4, 8, then every 15 minutes
			p.later = now.Add(min(time.Minute<<p.refused, 15*time.Minute))
			p.refused = min(p.refused+1, 4)
		}
		if e, ok := errors.AsType[*apiError](err); ok && e.Retry > 0 {
			p.until = now.Add(e.Retry)
		}
		if st.Problem != was { // once, not every minute Discord stays down
			p.Log.Warn("discord status message", "err", err)
		}
		p.setStatus(st)
		return
	}
	p.sent, p.refused = string(body), 0
	st := Status{Channel: pst.Name, Updated: now.UnixMilli()}
	if pst.Guild != "" {
		st.Link = "https://discord.com/channels/" + pst.Guild + "/" + pst.Channel + "/" + pst.Message
	}
	if err != nil {
		st.Problem, _ = problem(err)
	}
	p.setStatus(st)
}

// send edits the message in set's channel, or posts it there when there is
// none yet or someone deleted it.
func (p *Poster) send(ctx context.Context, set Settings, e embed) (posted, error) {
	pst := p.loadPosted(ctx)
	msg := message{Embeds: []embed{e}, AllowedMentions: noMentions}
	var got struct {
		ID     string            `json:"id"`
		Embeds []json.RawMessage `json:"embeds"`
	}
	if pst.Channel == set.Channel && pst.Message != "" {
		err := p.call(ctx, set.Token, http.MethodPatch, "/channels/"+pst.Channel+"/messages/"+pst.Message, msg, &got)
		if err == nil {
			return pst, withoutEmbed(len(got.Embeds))
		}
		if e, ok := errors.AsType[*apiError](err); !ok || e.Code != unknownMessage {
			return pst, err
		}
	}
	// A new message: look up the channel first, for its name and a link to
	// the message, and to tell a wrong ID from missing permissions.
	var ch struct {
		Name  string `json:"name"`
		Guild string `json:"guild_id"`
	}
	if err := p.call(ctx, set.Token, http.MethodGet, "/channels/"+set.Channel, nil, &ch); err != nil {
		return pst, err
	}
	if err := p.call(ctx, set.Token, http.MethodPost, "/channels/"+set.Channel+"/messages", msg, &got); err != nil {
		return pst, err
	}
	pst = posted{Channel: set.Channel, Message: got.ID, Guild: ch.Guild, Name: ch.Name}
	b, _ := json.Marshal(pst)
	if err := p.Store.Set(ctx, postedKey, string(b)); err != nil {
		return pst, err
	}
	return pst, withoutEmbed(len(got.Embeds))
}

func withoutEmbed(n int) error {
	if n == 0 {
		return errNoEmbed
	}
	return nil
}

var errNoEmbed = errors.New("the message went out without its embed")

func (p *Poster) loadPosted(ctx context.Context) posted {
	var pst posted
	if v, err := p.Store.Get(ctx, postedKey); err == nil {
		_ = json.Unmarshal([]byte(v), &pst)
	}
	return pst
}

// Retire deletes the message that old posted, before settings that move it to
// another channel or bot, or turn it off, are saved: left there, it would
// show the servers as they were for good. A failure is only logged; the
// message can be deleted in Discord.
func (p *Poster) Retire(ctx context.Context, old Settings) {
	p.run.Lock()
	defer p.run.Unlock()
	pst := p.loadPosted(ctx)
	if pst.Message == "" {
		return
	}
	if old.Token != "" && pst.Channel == old.Channel {
		err := p.call(ctx, old.Token, http.MethodDelete, "/channels/"+pst.Channel+"/messages/"+pst.Message, nil, nil)
		if e, ok := errors.AsType[*apiError](err); err != nil && (!ok || e.Code != unknownMessage) {
			p.Log.Warn("delete the old discord status message", "err", err)
		}
	}
	if err := p.Store.Set(ctx, postedKey, "{}"); err != nil {
		p.Log.Warn("forget the discord status message", "err", err)
	}
	p.sent = ""
	p.setStatus(Status{})
}

// Test posts a test message with token in channel, before they're saved, and
// returns the channel's name.
func (p *Poster) Test(ctx context.Context, token, channel string) (string, error) {
	var ch struct {
		Name string `json:"name"`
	}
	if err := p.call(ctx, token, http.MethodGet, "/channels/"+channel, nil, &ch); err != nil {
		text, _ := problem(err)
		return "", errors.New(text)
	}
	e := embed{Title: "Test from ReSkateManager", Color: blue,
		Description: "The servers' status will show here, in one message the manager keeps up to date.",
		Footer:      &footer{Text: footerText}}
	var got struct {
		Embeds []json.RawMessage `json:"embeds"`
	}
	err := p.call(ctx, token, http.MethodPost, "/channels/"+channel+"/messages", message{Embeds: []embed{e}, AllowedMentions: noMentions}, &got)
	if err == nil {
		err = withoutEmbed(len(got.Embeds))
	}
	if err != nil {
		text, _ := problem(err)
		return ch.Name, errors.New(text)
	}
	return ch.Name, nil
}

// farewell marks every server offline as the manager stops.
func (p *Poster) farewell() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.run.Lock()
	defer p.run.Unlock()
	set, err := Load(ctx, p.Store)
	if err != nil || set.Token == "" || set.Channel == "" || p.stopFor == set.Token+" "+set.Channel {
		return
	}
	pst := p.loadPosted(ctx)
	if pst.Channel != set.Channel || pst.Message == "" {
		return
	}
	now := time.Now()
	e := p.render(set, true, now)
	e.Description += fmt.Sprintf("\n\nUpdated <t:%d:R>", now.Unix())
	err = p.call(ctx, set.Token, http.MethodPatch, "/channels/"+pst.Channel+"/messages/"+pst.Message, message{Embeds: []embed{e}, AllowedMentions: noMentions}, nil)
	if err != nil {
		p.Log.Warn("discord status message at shutdown", "err", err)
	}
}

// unknownMessage is Discord's code for a message that isn't there, such as
// one someone deleted.
const unknownMessage = 10008

// apiError is a request Discord turned down.
type apiError struct {
	Status  int
	Code    int // Discord's own, such as unknownMessage
	Message string
	Retry   time.Duration // how long a rate limit asks to wait
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("discord: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("discord: HTTP %d", e.Status)
}

// retry is when to try again after a failure.
type retry int

const (
	retrySoon  retry = iota // in a minute
	retrySlow               // backing off: it's put right in Discord, not here
	retryNever              // not until the settings change
)

// problem says what went wrong, for the Manager page, and when to try again.
func problem(err error) (string, retry) {
	if errors.Is(err, errNoEmbed) {
		return "The message went out without its embed: give the bot Embed Links in that channel.", retrySoon
	}
	e, ok := errors.AsType[*apiError](err)
	switch {
	case !ok:
		return "Couldn't reach Discord (" + err.Error() + "); trying again in a minute.", retrySoon
	case e.Status == http.StatusUnauthorized:
		return "Discord refused the bot token. If it was reset in the Developer Portal, save the new one.", retryNever
	case e.Status == http.StatusForbidden:
		return "The bot can't see that channel, or post in it. Invite it to the Discord server, and give it View Channel, Send Messages and Embed Links there; it checks again every few minutes.", retrySlow
	case e.Status == http.StatusNotFound:
		return "Discord has no channel with that ID. Check that it's the channel's ID, not the server's.", retryNever
	case e.Status == http.StatusTooManyRequests:
		return "Discord asked the bot to slow down; trying again shortly.", retrySoon
	case e.Status >= 500:
		return fmt.Sprintf("Discord isn't working right now (HTTP %d); trying again in a minute.", e.Status), retrySoon
	}
	return "Discord turned the message down: " + e.Error(), retrySoon
}

// call sends one request to Discord as the bot, and decodes its answer into
// out, when given.
func (p *Poster) call(ctx context.Context, token, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	api := p.API
	if api == "" {
		api = DefaultAPI
	}
	req, err := http.NewRequestWithContext(ctx, method, api+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+token)
	// Discord asks bots to name themselves this way.
	req.Header.Set("User-Agent", "DiscordBot (https://github.com/xThrasherrr/ReSkateManager, "+p.Version+")")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// The token rides in a header a redirect would carry somewhere else.
	cl := *client
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return fmt.Errorf("discord: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 300 {
		if out == nil || resp.StatusCode == http.StatusNoContent {
			return nil
		}
		return json.Unmarshal(b, out)
	}
	var d struct {
		Message    string  `json:"message"`
		Code       int     `json:"code"`
		RetryAfter float64 `json:"retry_after"`
	}
	_ = json.Unmarshal(b, &d)
	e := &apiError{Status: resp.StatusCode, Code: d.Code, Message: d.Message}
	if resp.StatusCode == http.StatusTooManyRequests {
		// In seconds, in the header and the body.
		wait := 5 * time.Second
		if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil {
			wait = time.Duration(s * float64(time.Second))
		} else if d.RetryAfter > 0 {
			wait = time.Duration(d.RetryAfter * float64(time.Second))
		}
		e.Retry = min(max(wait, time.Second), 10*time.Minute)
	}
	return e
}
