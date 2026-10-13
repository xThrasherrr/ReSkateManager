package instance

import (
	"context"
	"errors"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// Maps lists the maps the running server can load, retail and custom from
// Mods. It asks once per run, and again once the server has read Mods again
// (ReSkate 2.0.7), and keeps the question out of the console.
func (in *Instance) Maps(ctx context.Context) ([]string, error) {
	in.mu.Lock()
	cached, gen := in.maps, in.mapsGen
	in.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	reply, err := in.send(ctx, "maps", "", true, replyShape(logparse.MapsHeadRe))
	if err != nil {
		return nil, err
	}
	list, ok := logparse.ParseMaps(reply)
	if !ok {
		return nil, errors.New("unexpected reply to maps")
	}
	in.mu.Lock()
	if in.mapsGen == gen { // not read again while this was asked
		in.maps = list
	}
	in.mu.Unlock()
	return list, nil
}

// ErrNoMapsReload is a server older than ReSkate 2.0.7, which reads Mods only
// when it starts. It answers maps reload with its maps.
var ErrNoMapsReload = errors.New("this server reads its mods only when it starts (ReSkate 2.0.7 reads them while it runs); restart it to apply them")

// ReloadMaps has the running server read Mods again (maps reload, ReSkate
// 2.0.7), so maps added or taken out since it started can be switched to at
// once. The reply says how many maps it knows and which are new.
func (in *Instance) ReloadMaps(ctx context.Context, by string) (string, error) {
	reply, err := in.send(ctx, "maps reload", by, false, func(text string) bool {
		first, _, _ := strings.Cut(text, "\n")
		return strings.HasPrefix(text, logparse.MapsReread) || logparse.MapsHeadRe.MatchString(first) || logparse.IsCommandError(text)
	})
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(reply, logparse.MapsReread) {
		return "", ErrNoMapsReload
	}
	return reply, nil
}
