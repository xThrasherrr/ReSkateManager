package instance

import "sync"

// Event is what a panel subscriber receives over its WebSocket.
type Event struct {
	Type     string   `json:"type"` // console | state | players
	Instance string   `json:"instance"`
	Entry    any      `json:"entry,omitempty"`
	State    *View    `json:"state,omitempty"`
	Players  []Player `json:"players,omitempty"`
}

// hub fans events out to subscribers. A subscriber that falls behind is
// dropped rather than allowed to stall the server's output.
type hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func (h *hub) subscribe() chan Event {
	ch := make(chan Event, 512)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan Event]struct{}{}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}
