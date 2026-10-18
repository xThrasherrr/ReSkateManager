package instance

import (
	"context"
	"errors"

	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// Locks lists the players whose tools are locked (locks, ReSkate 2.1.0),
// those who left too, and keeps the question out of the console.
func (in *Instance) Locks(ctx context.Context) ([]logparse.Lock, error) {
	reply, err := in.send(ctx, "locks", "", true, replyShape(logparse.LocksHeadRe))
	if err != nil {
		return nil, err
	}
	list, ok := logparse.ParseLocks(reply)
	if !ok {
		return nil, errors.New("unexpected reply to locks")
	}
	return list, nil
}
