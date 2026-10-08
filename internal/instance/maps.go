package instance

import (
	"context"
	"errors"
	"regexp"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

var mapsReplyRe = regexp.MustCompile(`^\d+ maps\b`)

// Maps lists the maps the running server can load, retail and custom from
// Mods. It asks once per run (Mods only change across restarts) and keeps the
// question out of the console.
func (in *Instance) Maps(ctx context.Context) ([]string, error) {
	in.mu.Lock()
	cached := in.maps
	in.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	reply, err := in.send(ctx, "maps", "", true, replyShape(mapsReplyRe))
	if err != nil {
		return nil, err
	}
	list, ok := logparse.ParseMaps(reply)
	if !ok {
		return nil, errors.New("unexpected reply to maps")
	}
	in.mu.Lock()
	in.maps = list
	in.mu.Unlock()
	return list, nil
}
