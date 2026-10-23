package instance

import (
	"context"
	"errors"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// SpawnedParks lists the park mods that are up (park-mod list, ReSkate
// 2.0.6), and keeps the question out of the console.
func (in *Instance) SpawnedParks(ctx context.Context) ([]logparse.SpawnedPark, error) {
	reply, err := in.send(ctx, "park-mod list", "", true, replyShape(logparse.ParksHeadRe))
	if err != nil {
		return nil, err
	}
	list, ok := logparse.ParseParks(reply)
	if !ok {
		return nil, errors.New("unexpected reply to park-mod list")
	}
	return list, nil
}
