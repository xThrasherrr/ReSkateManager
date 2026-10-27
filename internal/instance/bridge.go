package instance

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// bridge is what the manager knows this run of each ReSkate plugin that
// talks to its plugins, by the plugin's id.
type bridge map[string]*bridged

type bridged struct {
	key     string    // the one it was handed, "" before its first hello
	hello   time.Time // the last hello answered
	dropped bool      // a line without its key has been noted in the console
}

const (
	// helloEvery is the least time between two hellos answered for a plugin.
	// Its own come 10 s apart until it has its key, so more are lines made to
	// look like its, and each answered sends a command.
	helloEvery = 5 * time.Second
	// bridgeMost is how many plugins the manager keeps a key for in a run.
	// The ids come from lines anyone's plugin can print, so there's no other
	// end to them.
	bridgeMost = 64
	keyTime    = 15 * time.Second // the longest a key waits for its turn and the reply
)

// bridgeLocked takes a line a ReSkate plugin wrote for the manager's
// plugins. A hello is answered with the plugin's key, sent to its console
// command "rsm <id>" out of the console's view, and passed on so the
// manager's plugins can send it what it needs again. A message is passed on
// only with that key: any plugin can print a line that reads like
// another's, and one that logs a player's text can be made to, but only the
// plugin itself is given its key. Reports whether to keep the line out of
// the console: every message is, passed on or not, as it carries a key and
// nothing a person reads; the first one dropped is noted instead.
func (in *Instance) bridgeLocked(text string) bool {
	b, ok := logparse.ParseBridge(text)
	if !ok || in.OnNotice == nil {
		return false
	}
	p := in.bridge[b.Plugin]
	if p == nil {
		if len(in.bridge) >= bridgeMost {
			return false
		}
		if in.bridge == nil {
			in.bridge = bridge{}
		}
		p = &bridged{}
		in.bridge[b.Plugin] = p
	}
	if b.Key == "" {
		now := time.Now()
		if now.Sub(p.hello) < helloEvery {
			return false
		}
		if p.key == "" {
			p.key = rand.Text()
		}
		p.hello = now
		go in.sendKey(b.Plugin, p.key)
		in.noticeLocked(Notice{Kind: NoticeReSkate, Plugin: b.Plugin})
		return false
	}
	if p.key == "" || subtle.ConstantTimeCompare([]byte(b.Key), []byte(p.key)) != 1 {
		if !p.dropped {
			p.dropped = true
			in.managerLineLocked("Dropped a message from the server plugin " + b.Plugin +
				" that didn't carry the key the manager gave it. Later ones aren't noted until the server restarts.")
		}
		return true
	}
	in.noticeLocked(Notice{Kind: NoticeReSkate, Plugin: b.Plugin, Text: b.Data})
	return true
}

// sendKey hands a ReSkate plugin its key. One that doesn't get it, as the
// server is still starting, says hello again.
func (in *Instance) sendKey(plugin, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), keyTime)
	defer cancel()
	if _, err := in.send(ctx, "rsm "+plugin+" key "+key, "", true, nil); err != nil {
		in.log.Debug("a server plugin's key wasn't sent", "plugin", plugin, "err", err)
	}
}
