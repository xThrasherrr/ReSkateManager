package instance

import (
	"context"
	"errors"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// ServerPlugins lists the plugins the server runs from its Plugins folder
// (plugin list, ReSkate 2.1.2), and keeps the question out of the console.
func (in *Instance) ServerPlugins(ctx context.Context) ([]logparse.ServerPlugin, error) {
	reply, err := in.send(ctx, "plugin list", "", true, replyShape(logparse.PluginsHeadRe))
	if err != nil {
		return nil, err
	}
	list, ok := logparse.ParsePlugins(reply)
	if !ok {
		return nil, errors.New("unexpected reply to plugin list")
	}
	return list, nil
}
