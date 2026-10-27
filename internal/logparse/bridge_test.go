package logparse

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseBridge(t *testing.T) {
	const key = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	data := `{"t":"add","board":"race.wins","n":1}`
	b64 := base64.StdEncoding.EncodeToString([]byte(data))
	for _, c := range []struct {
		line string
		want Bridge
	}{
		{"[Plugins] [someone-race_timer] rsm hello", Bridge{Plugin: "someone-race_timer"}},
		{"[Plugins] [races] rsm " + key + " " + b64, Bridge{Plugin: "races", Key: key, Data: data}},
		// Without its padding, as some Lua base64s write it.
		{"[Plugins] [races] rsm " + key + " " + strings.TrimRight(base64.StdEncoding.EncodeToString([]byte("[1]")), "="), Bridge{Plugin: "races", Key: key, Data: "[1]"}},
		{"[Plugins] [races] rsm " + key + " " + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("[", BridgeDepth)+strings.Repeat("]", BridgeDepth))),
			Bridge{Plugin: "races", Key: key, Data: strings.Repeat("[", BridgeDepth) + strings.Repeat("]", BridgeDepth)}},
	} {
		if got, ok := ParseBridge(c.line); !ok || got != c.want {
			t.Errorf("%q: %+v %v, want %+v", c.line, got, ok, c.want)
		}
	}

	for _, line := range []string{
		"[Plugins] [races] rsm " + key,                                    // no message
		"[Plugins] [races] rsm " + key + " " + b64 + " extra",             // more after it
		"[Plugins] [races] rsm " + key + " " + b64[:len(b64)-6] + " ...",  // cut at 2000 characters
		"[Plugins] [races] rsm " + key + " " + b64 + "\n[Plugins] [x] hi", // a line after it
		"[Plugins] [races] rsm abcdefghijklmnopqrstuvwxyz " + b64,         // not a key
		"[Plugins] [races] rsm " + key + " " + base64.StdEncoding.EncodeToString([]byte("{not json")),
		"[Plugins] [races] rsm " + key + " %%%%", // not base64
		"[Plugins] [races] rsm " + key + " " + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("[", BridgeDepth+1)+strings.Repeat("]", BridgeDepth+1))),
		"[Plugins] warning: [races] rsm hello",                  // a warning, not a message
		"[Plugins] [two words] rsm hello",                       // no command can be named after it
		"[Plugins] [" + strings.Repeat("a", 45) + "] rsm hello", // nor after this
		"[Plugins] [races] rsm hello again",
		"[chat] Bob: [Plugins] [races] rsm hello",
		"[Plugins] [races] RSM hello",
	} {
		if got, ok := ParseBridge(line); ok {
			t.Errorf("%q read as %+v", line, got)
		}
	}
}
