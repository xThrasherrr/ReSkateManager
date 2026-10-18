package discord

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

// message is what a bot sends: here, one embed and no pings.
type message struct {
	Embeds          []embed  `json:"embeds"`
	AllowedMentions mentions `json:"allowed_mentions"`
}

type mentions struct {
	Parse []string `json:"parse"`
}

// noMentions turns off every ping, should a name hold one.
var noMentions = mentions{Parse: []string{}}

func messageOf(e embed) message {
	return message{Embeds: []embed{e}, AllowedMentions: noMentions}
}

type embed struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Color       int     `json:"color"`
	Fields      []field `json:"fields,omitempty"`
	Footer      *footer `json:"footer,omitempty"`
}

type field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type footer struct {
	Text string `json:"text"`
}

// The colours of an embed's edge, shared with the alerts.
const (
	Green  = 0x7ee26b
	Yellow = 0xffd23f
	Red    = 0xff4f5c
	Blue   = 0x4cc3ff
	Grey   = 0x8a8f98
)

// Discord's limits on an embed, kept under with room to spare.
const (
	maxFields = 25
	maxSize   = 5500 // characters in all; Discord's limit is 6000
)

// server is one server as the message shows it.
type server struct {
	instance.View
	Release string // the ReSkate release it runs, when known
}

func (p *Poster) servers() []server {
	var out []server
	for _, in := range p.Reg.List() {
		s := server{View: in.View()}
		if p.Release != nil && s.State == instance.Running {
			s.Release = p.Release(s.Def.Exe())
		}
		out = append(out, s)
	}
	return out
}

func (p *Poster) render(set Settings, down bool, now time.Time) embed {
	return render(p.servers(), set, down, now)
}

// footerText signs every message the bot sends.
const footerText = "Powered by ReSkateManager"

// render is the status message's embed for these servers: a summary, then
// one field each. down shows them all offline, as the manager stops.
func render(list []server, set Settings, down bool, now time.Time) embed {
	e := embed{Title: cmp.Or(set.Title, DefaultTitle), Footer: &footer{Text: footerText}}
	online, players := 0, 0
	var fields []field
	for _, s := range list {
		if down {
			s.State = instance.Stopped
		}
		if s.State == instance.Running {
			online++
			players += s.Players
		}
		fields = append(fields, serverField(s, set))
	}
	switch {
	case down:
		e.Color = Grey
		e.Description = fmt.Sprintf("The manager stopped <t:%d:R>, so the servers are offline until it's back.", now.Unix())
	case len(list) == 0:
		e.Color = Grey
		e.Description = "No servers yet."
	default:
		e.Color = Red
		if online == len(list) {
			e.Color = Green
		} else if online > 0 {
			e.Color = Yellow
		}
		e.Description = fmt.Sprintf("**%d of %s online**", online, plural(len(list), "server"))
		if online > 0 {
			e.Description += fmt.Sprintf(" · %s on", plural(players, "player"))
		}
	}
	size := len([]rune(e.Title + e.Description + e.Footer.Text))
	for i, f := range fields {
		n := len([]rune(f.Name + f.Value))
		more := len(fields) - i
		if len(e.Fields) == maxFields-1 && more > 1 || size+n > maxSize {
			e.Description += fmt.Sprintf("\n…and %d more not shown.", more)
			break
		}
		e.Fields = append(e.Fields, f)
		size += n
	}
	return e
}

var stateEmoji = map[instance.State]string{
	instance.Running:  "🟢",
	instance.Starting: "🟡",
	instance.Updating: "🟡",
	instance.Stopping: "🟠",
	instance.Crashed:  "🔴",
	instance.Stopped:  "⚫",
}

var stateLabel = map[instance.State]string{
	instance.Running:  "Online",
	instance.Starting: "Starting",
	instance.Updating: "Updating",
	instance.Stopping: "Stopping",
	instance.Crashed:  "Crashed",
	instance.Stopped:  "Offline",
}

// serverField is one server's state, and while it runs its players, map,
// join code, release, uptime and next restart.
func serverField(s server, set Settings) field {
	f := field{Name: Truncate(stateEmoji[s.State]+" "+Escape(s.Def.Name), 256)}
	label := stateLabel[s.State]
	if label == "" {
		label = string(s.State)
	}
	if s.State != instance.Running {
		f.Value = "**" + label + "**"
		return f
	}
	line := "**" + label + "** · "
	if s.Info.MaxPlayers > 0 {
		line += fmt.Sprintf("%d/%d players", s.Players, s.Info.MaxPlayers)
	} else {
		line += plural(s.Players, "player")
	}
	if s.Info.Map != "" {
		line += " · " + Escape(Truncate(s.Info.Map, 100))
	}
	lines := []string{line}

	var join []string
	if code := strings.ReplaceAll(s.Info.JoinCode, "`", ""); set.JoinCodes && code != "" {
		join = append(join, "Join code `"+Truncate(code, 64)+"`")
	}
	if s.Info.Password {
		join = append(join, "🔒 Password required")
	}
	if len(join) > 0 {
		lines = append(lines, strings.Join(join, " · "))
	}

	var run []string
	if s.Release != "" {
		run = append(run, "ReSkate "+Escape(s.Release))
	}
	if s.ReadyAt > 0 {
		run = append(run, fmt.Sprintf("started <t:%d:R>", s.ReadyAt/1000))
	}
	if s.NextRestart > 0 {
		run = append(run, fmt.Sprintf("restarts <t:%d:R>", s.NextRestart/1000))
	}
	if len(run) > 0 {
		run[0] = strings.ToUpper(run[0][:1]) + run[0][1:]
		lines = append(lines, strings.Join(run, " · "))
	}
	f.Value = Truncate(strings.Join(lines, "\n"), 1024)
	return f
}

var markdown = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`, "[", `\[`, "<", `\<`, ">", `\>`, "#", `\#`)

// Escape escapes what would format a name, a map's title or a plugin's post
// in Discord, or make it a mention or a timestamp.
func Escape(s string) string { return markdown.Replace(s) }

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// Truncate cuts s to n characters, the last of them an ellipsis, to fit one
// of Discord's limits.
func Truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
