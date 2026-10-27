package logparse

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
)

// Bridge is a line a ReSkate plugin on the server writes for the manager's
// plugins, with reskate.log:
//
//	[Plugins] [<id>] rsm hello
//	[Plugins] [<id>] rsm <key> <base64 JSON>
//
// The first as it starts, until the manager hands it its key; then each
// message with that key. ReSkate writes the [<id>] itself
// (Extension/Plugins/plugin_host.cpp, Host::log), but reskate.console.print
// writes any line, so only the key tells a plugin's messages from lines made
// to look like them. Key and Data are "" for a hello.
type Bridge struct {
	Plugin string
	Key    string
	Data   string // the JSON, decoded from base64 and checked
}

// The id is what a console command can be named after: "rsm <id>" in 48
// characters, matched without case. The key is crypto/rand.Text's. The line
// must match to its end, so a message cut at ReSkate's 2000 characters, or
// one with a line after it, is no message.
var bridgeRe = regexp.MustCompile(`^\[Plugins\] \[([A-Za-z0-9_-]{1,44})\] rsm (?:hello|([A-Z2-7]{26}) ([A-Za-z0-9+/]+={0,2}))$`)

// BridgeDepth is how many tables deep a message may go: a plugin's event
// holds it, and the Lua bridge takes 16 at most.
const BridgeDepth = 8

// ParseBridge reads a ReSkate plugin's line for the manager's plugins.
func ParseBridge(text string) (Bridge, bool) {
	m := bridgeRe.FindStringSubmatch(text)
	if m == nil {
		return Bridge{}, false
	}
	if m[2] == "" {
		return Bridge{Plugin: m[1]}, true
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(m[3], "="))
	if err != nil {
		return Bridge{}, false
	}
	var v any
	if json.Unmarshal(data, &v) != nil || depth(v) > BridgeDepth {
		return Bridge{}, false
	}
	return Bridge{Plugin: m[1], Key: m[2], Data: string(data)}, true
}

// depth is how many tables deep v goes.
func depth(v any) int {
	d := 0
	switch v := v.(type) {
	case []any:
		for _, x := range v {
			d = max(d, depth(x))
		}
	case map[string]any:
		for _, x := range v {
			d = max(d, depth(x))
		}
	default:
		return 0
	}
	return d + 1
}
