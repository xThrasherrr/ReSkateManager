package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/plugins/luavm"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
	"github.com/xThrasherrr/ReSkateManager/internal/supervisor"
)

// What plugins keep: each one's store, the boards they define and the scores
// they add. The calls answer from memory, so none waits on the database's one
// writer, which the panel and the audit log share: changes are written within
// flushEvery, a burst of them in batches, and boards are read from snapshots
// worked out on the database's readers.
const (
	maxKey     = 256      // bytes of a store key
	maxValue   = 64 << 10 // bytes of one stored value, as kept
	maxStore   = 1 << 20  // bytes of a plugin's whole store, keys and values
	maxWaiting = 10000    // scores a plugin has waiting to be written
	maxRows    = 100      // rows a board call returns
	maxTitle   = 64       // bytes of a board's title
	maxUnit    = 32
	maxSnaps   = 64 // boards' snapshots kept at once

	flushEvery = time.Second
	flushBatch = 200              // scores to a transaction, so other writes get turns between
	snapAge    = 30 * time.Second // a snapshot stands this long while nothing is added to its board
	snapIdle   = 5 * time.Minute  // one nobody read for this long goes
)

var (
	readWait = 50 * time.Millisecond // the longest a call waits on the database
	boardRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,63}$`)
	errBusy  = errors.New("the database is busy; try again in a moment")
)

// data is what the plugins keep, for their Manager.
type data struct {
	st       *store.Store
	log      *slog.Logger
	ctx      context.Context // done once it closes
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	flushing chan struct{} // held while writing

	mu      sync.Mutex // guards what follows
	stores  map[string]*kv
	boards  map[string]store.Board
	defined map[string]store.Board    // defined or changed since they were last written
	scores  map[scoreKey]*store.Score // waiting to be written, each merged with the others for its rows
	waiting map[string]int            // of those, how many each plugin has
	snaps   map[snapKey]*snapshot
}

// kv is a plugin's store.
type kv struct {
	vals  map[string][]byte
	size  int
	dirty map[string]bool // changed since last written
}

type scoreKey struct {
	board, server, steamID string
	month, week            int64
}

func keyOf(s store.Score) scoreKey {
	at := time.Unix(s.At, 0)
	return scoreKey{s.Board, s.Server, s.SteamID, store.PeriodStart("month", at), store.PeriodStart("week", at)}
}

type snapKey struct {
	board, server, period string
	starts                int64
}

// snapshot is a board's ranking as last worked out.
type snapshot struct {
	cur   *ranking
	err   error // why it last failed
	at    time.Time
	used  time.Time
	added uint64        // counts adds to its board
	seen  uint64        // added, as cur was worked out
	ready chan struct{} // closed once the working out under way ends; nil while none is
}

type ranking struct {
	list []store.Ranked
	pos  map[string]int // each player's place in list
}

func newData(st *store.Store, log *slog.Logger) *data {
	d := &data{st: st, log: log, flushing: make(chan struct{}, 1), stores: map[string]*kv{}, boards: map[string]store.Board{},
		defined: map[string]store.Board{}, scores: map[scoreKey]*store.Score{}, waiting: map[string]int{}, snaps: map[snapKey]*snapshot{}}
	d.ctx, d.cancel = context.WithCancel(context.Background())
	return d
}

// openData reads the boards plugins defined, and starts writing what they
// keep.
func openData(ctx context.Context, st *store.Store, log *slog.Logger) (*data, error) {
	list, err := st.Boards(ctx)
	if err != nil {
		return nil, err
	}
	d := newData(st, log)
	for _, b := range list {
		d.boards[b.ID] = b
	}
	d.wg.Add(1)
	go d.run()
	return d, nil
}

// close writes what waits, once the plugins have stopped.
func (d *data) close() {
	d.cancel()
	d.wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.flush(ctx, ""); err != nil {
		d.log.Error("plugins: what they keep wasn't all saved", "err", err)
	}
}

func (d *data) run() {
	defer d.wg.Done()
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
		}
		if err := d.flush(d.ctx, ""); err != nil && d.ctx.Err() == nil {
			d.log.Warn("plugins: saving what they keep", "err", err)
		}
		d.mu.Lock()
		for k, sn := range d.snaps {
			if sn.ready == nil && time.Since(sn.used) > snapIdle {
				delete(d.snaps, k)
			}
		}
		d.mu.Unlock()
	}
}

// flush writes what waits: boards defined, then scores, flushBatch to a
// transaction, then the stores' changes; given a board, only its scores.
// What it couldn't write waits for the next.
func (d *data) flush(ctx context.Context, board string) error {
	select {
	case d.flushing <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-d.flushing }()
	d.mu.Lock()
	defined := slices.Collect(maps.Values(d.defined))
	clear(d.defined)
	d.mu.Unlock()
	if len(defined) > 0 {
		if err := d.st.SaveBoards(ctx, defined); err != nil {
			d.mu.Lock()
			for _, b := range defined {
				if _, ok := d.defined[b.ID]; !ok {
					d.defined[b.ID] = b
				}
			}
			d.mu.Unlock()
			return err
		}
	}
	for {
		d.mu.Lock()
		var batch []store.Score
		for k, s := range d.scores {
			if board != "" && k.board != board {
				continue
			}
			batch = append(batch, *s)
			delete(d.scores, k)
			d.waiting[d.boards[k.board].Plugin]--
			if len(batch) == flushBatch {
				break
			}
		}
		d.mu.Unlock()
		if len(batch) == 0 {
			break
		}
		if err := d.st.AddScores(ctx, batch); err != nil {
			d.mu.Lock()
			for _, s := range batch {
				d.queueLocked(s)
			}
			d.mu.Unlock()
			return err
		}
	}
	if board != "" {
		return nil
	}
	d.mu.Lock()
	changed := map[string]map[string][]byte{}
	for id, s := range d.stores {
		if len(s.dirty) > 0 {
			c := map[string][]byte{}
			for k := range s.dirty {
				c[k] = s.vals[k] // nil once deleted
			}
			clear(s.dirty)
			changed[id] = c
		}
	}
	d.mu.Unlock()
	for id, c := range changed {
		if err := d.st.SavePluginStore(ctx, id, c); err != nil {
			d.mu.Lock()
			for left, keys := range changed { // this one and those not tried
				for k := range keys {
					d.stores[left].dirty[k] = true
				}
			}
			d.mu.Unlock()
			return err
		}
		delete(changed, id)
	}
	return nil
}

// ---- stores ----

// load reads a plugin's store, unless it has been already.
func (d *data) load(id string) error {
	d.mu.Lock()
	_, ok := d.stores[id]
	d.mu.Unlock()
	if ok {
		return nil
	}
	vals, err := d.st.PluginStore(d.ctx, id)
	if err != nil {
		return err
	}
	s := &kv{vals: vals, dirty: map[string]bool{}}
	for k, v := range vals {
		s.size += len(k) + len(v)
	}
	d.mu.Lock()
	if _, ok := d.stores[id]; !ok {
		d.stores[id] = s
	}
	d.mu.Unlock()
	return nil
}

func (d *data) get(id, key string) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stores[id].vals[key]
}

// set keeps v at key in a plugin's store, nil deleting it, and reports
// whether the key was there.
func (d *data) set(id, key string, v []byte) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.stores[id]
	old, had := s.vals[key]
	size := s.size
	if had {
		size -= len(key) + len(old)
	}
	if v != nil {
		size += len(key) + len(v)
	}
	if size > maxStore && size > s.size {
		return had, fmt.Errorf("store.set: the plugin's store holds %d MB at most", maxStore>>20)
	}
	if v == nil {
		delete(s.vals, key)
	} else {
		s.vals[key] = v
	}
	s.size = size
	if had || v != nil {
		s.dirty[key] = true
	}
	return had, nil
}

func (d *data) keys(id, prefix string) []any {
	d.mu.Lock()
	var keys []string
	for k := range d.stores[id].vals {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	d.mu.Unlock()
	slices.Sort(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

// ---- boards ----

// board is a board by its ID: playtime, or one a plugin defined.
func (d *data) board(id string) (store.Board, bool) {
	if id == store.PlaytimeBoard {
		return store.Board{ID: id, Title: "Playtime", Unit: "seconds", Keep: store.KeepSum}, true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.boards[id]
	return b, ok
}

// define keeps a board as a plugin defines it. A board is the first plugin's
// to define it, and keeps its scores the one way for good.
func (d *data) define(b store.Board) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.boards[b.ID]; ok {
		switch {
		case old.Plugin != b.Plugin:
			return fmt.Errorf("board %q is plugin %q's", b.ID, old.Plugin)
		case old.Keep != b.Keep:
			return fmt.Errorf("board %q is kept as %s; that can't change, so give it a new ID", b.ID, old.Keep)
		case old == b:
			return nil
		}
	}
	d.boards[b.ID] = b
	d.defined[b.ID] = b
	return nil
}

// add queues a score to be written; false when the plugin has maxWaiting.
func (d *data) add(plugin string, s store.Score) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.scores[keyOf(s)]; !ok && d.waiting[plugin] >= maxWaiting {
		return false
	}
	d.queueLocked(s)
	for k, sn := range d.snaps {
		if k.board == s.Board {
			sn.added++
		}
	}
	return true
}

func (d *data) queueLocked(s store.Score) {
	k := keyOf(s)
	if w, ok := d.scores[k]; ok {
		merge(w, s)
		return
	}
	d.scores[k] = &s
	d.waiting[d.boards[s.Board].Plugin]++
}

// merge folds n into s, a score for the same rows, either being the older.
func merge(s *store.Score, n store.Score) {
	if s.Keep == store.KeepSum {
		s.Value += n.Value
		s.At = max(s.At, n.At)
		return
	}
	better := n.Value > s.Value
	if s.Keep == store.KeepFastest {
		better = n.Value < s.Value
	}
	switch {
	case better:
		s.Value, s.At = n.Value, n.At
	case n.Value == s.Value:
		s.At = min(s.At, n.At)
	}
}

// ranking is a board's players in order, from its snapshot. One added to
// since is worked out afresh, waiting up to readWait for it and making do
// with the one before past that; one that's only old is worked out again in
// the background.
func (d *data) ranking(b store.Board, server, period string) (*ranking, error) {
	now := time.Now()
	k := snapKey{b.ID, server, period, store.PeriodStart(period, now)}
	d.mu.Lock()
	if d.ctx.Err() != nil {
		d.mu.Unlock()
		return nil, errBusy
	}
	sn := d.snaps[k]
	if sn == nil {
		d.evictLocked()
		sn = &snapshot{}
		d.snaps[k] = sn
	}
	sn.used = now
	added := sn.added != sn.seen
	if sn.cur != nil && !added && now.Sub(sn.at) < snapAge {
		defer d.mu.Unlock()
		return sn.cur, nil
	}
	if sn.ready == nil {
		sn.ready = make(chan struct{})
		d.wg.Add(1)
		go d.work(k, b, sn)
	}
	ready, cur := sn.ready, sn.cur
	d.mu.Unlock()
	if cur != nil && !added {
		return cur, nil
	}
	select {
	case <-ready:
	case <-time.After(readWait):
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case sn.cur != nil:
		return sn.cur, nil
	case sn.err != nil:
		return nil, sn.err
	}
	return nil, errBusy
}

// work works out a board's snapshot, writing the board's waiting scores first.
func (d *data) work(k snapKey, b store.Board, sn *snapshot) {
	defer d.wg.Done()
	d.mu.Lock()
	seen := sn.added
	d.mu.Unlock()
	err := d.flush(d.ctx, b.ID)
	var list []store.Ranked
	if err == nil {
		list, err = d.st.Ranking(d.ctx, store.BoardQuery{Board: b, Server: k.server, Period: k.period, Now: time.Now()})
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		r := &ranking{list: list, pos: make(map[string]int, len(list))}
		for i, x := range list {
			r.pos[x.SteamID] = i
		}
		sn.cur, sn.at, sn.seen, sn.err = r, time.Now(), seen, nil
	} else {
		sn.err = err
	}
	close(sn.ready)
	sn.ready = nil
}

// evictLocked makes room for a snapshot, dropping the one read longest ago.
func (d *data) evictLocked() {
	if len(d.snaps) < maxSnaps {
		return
	}
	var oldest *snapKey
	for k, sn := range d.snaps {
		if sn.ready == nil && (oldest == nil || sn.used.Before(d.snaps[*oldest].used)) {
			oldest = &k
		}
	}
	if oldest != nil {
		delete(d.snaps, *oldest)
	}
}

// ---- the plugin's calls ----

// dataCall answers store.*, playtime.* and stats.*.
func (r *runner) dataCall(name string, args []any) ([]any, error) {
	arg := func(i int) any {
		if i < len(args) {
			return args[i]
		}
		return nil
	}
	d, id := r.m.data, r.p.ID
	switch name {
	case "store.get":
		key, ok := arg(0).(string)
		if !ok {
			return nil, badArg(1, name, "string", arg(0))
		}
		b := d.get(id, key)
		if b == nil {
			return []any{nil}, nil
		}
		v, err := luavm.Unmarshal(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %q doesn't read: %v", name, key, err)
		}
		return []any{v}, nil
	case "store.set", "store.delete":
		key, ok := arg(0).(string)
		if !ok || key == "" || len(key) > maxKey {
			return nil, fmt.Errorf("bad argument #1 to '%s' (a key of 1 to %d bytes)", name, maxKey)
		}
		var b []byte
		if name == "store.set" && arg(1) != nil {
			var err error
			if b, err = luavm.Marshal(arg(1)); err != nil {
				return nil, fmt.Errorf("bad argument #2 to '%s' (%v)", name, err)
			}
			if len(b) > maxValue {
				return nil, fmt.Errorf("bad argument #2 to '%s' (a value takes %d KB at most)", name, maxValue>>10)
			}
		}
		had, err := d.set(id, key, b)
		if err != nil || name == "store.set" {
			return nil, err
		}
		return []any{had}, nil
	case "store.keys":
		prefix, ok := arg(0).(string)
		if !ok && arg(0) != nil {
			return nil, badArg(1, name, "string", arg(0))
		}
		return []any{d.keys(id, prefix)}, nil
	case "playtime.total", "playtime.sessions":
		steam, err := steamID(name, 1, arg(0))
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.ctx, readWait)
		defer cancel()
		if name == "playtime.total" {
			server, ok := arg(1).(string)
			if !ok && arg(1) != nil {
				return nil, badArg(2, name, "server ID", arg(1))
			}
			n, err := d.st.Playtime(ctx, steam, server, time.Now().Unix())
			if err != nil {
				return []any{nil, busy(err)}, nil
			}
			return []any{n}, nil
		}
		limit, err := limitArg(name, 2, arg(1), 10)
		if err != nil {
			return nil, err
		}
		list, err := d.st.Visits(ctx, steam, limit)
		if err != nil {
			return []any{nil, busy(err)}, nil
		}
		out := make([]any, len(list))
		for i, v := range list {
			m := map[string]any{"server": v.Server, "joined_at": v.JoinedAt}
			if v.LeftAt != nil {
				m["left_at"], m["reason"] = *v.LeftAt, v.Reason
			}
			out[i] = m
		}
		return []any{out}, nil
	case "stats.define":
		bid, ok := arg(0).(string)
		switch {
		case !ok || !boardRe.MatchString(bid):
			return nil, fmt.Errorf("bad argument #1 to '%s' (a board ID: up to 64 of a-z, 0-9 and _.:-)", name)
		case bid == store.PlaytimeBoard:
			return nil, fmt.Errorf("bad argument #1 to '%s' (playtime is the manager's own board)", name)
		}
		opts, err := options(name, 2, arg(1), "title", "unit", "keep", "public")
		if err != nil {
			return nil, err
		}
		b := store.Board{ID: bid, Plugin: id, Title: bid, Keep: store.KeepSum}
		for _, o := range []struct {
			name string
			to   *string
			max  int
		}{{"title", &b.Title, maxTitle}, {"unit", &b.Unit, maxUnit}, {"keep", &b.Keep, 0}} {
			v, ok := opts[o.name]
			if !ok {
				continue
			}
			s, ok := v.(string)
			s = supervisor.CleanText(s)
			switch {
			case o.name == "keep" && !slices.Contains([]string{store.KeepSum, store.KeepBest, store.KeepFastest}, s):
				return nil, fmt.Errorf("bad argument #2 to '%s' (keep is \"sum\", \"best\" or \"fastest\")", name)
			case o.name != "keep" && (!ok || len(s) > o.max):
				return nil, fmt.Errorf("bad argument #2 to '%s' (%s is a string of %d bytes at most)", name, o.name, o.max)
			}
			*o.to = s
		}
		if b.Title == "" {
			b.Title = bid
		}
		if v, ok := opts["public"]; ok {
			if b.Public, ok = v.(bool); !ok {
				return nil, fmt.Errorf("bad argument #2 to '%s' (public is true or false)", name)
			}
		}
		return nil, d.define(b)
	case "stats.add":
		b, err := boardArg(d, name, arg(0))
		switch {
		case err != nil:
			return nil, fmt.Errorf("%w: define it with stats.define first", err)
		case b.ID == store.PlaytimeBoard:
			return nil, fmt.Errorf("bad argument #1 to '%s' (playtime adds itself up)", name)
		case b.Plugin != id:
			return nil, fmt.Errorf("bad argument #1 to '%s' (board %q is plugin %q's)", name, b.ID, b.Plugin)
		}
		in, err := r.server(name, 2, arg(1))
		if err != nil {
			return nil, err
		}
		steam, err := steamID(name, 3, arg(2))
		if err != nil {
			return nil, err
		}
		n, ok := number(arg(3))
		switch {
		case !ok:
			return nil, badArg(4, name, "number", arg(3))
		case math.IsNaN(n) || math.IsInf(n, 0):
			return nil, fmt.Errorf("bad argument #4 to '%s' (a number, not NaN or infinity)", name)
		}
		if !d.add(id, store.Score{Board: b.ID, Keep: b.Keep, Server: in.ID(), SteamID: steam, Value: n, At: time.Now().Unix()}) {
			return []any{false, fmt.Sprintf("%d scores are waiting to be written already", maxWaiting)}, nil
		}
		return []any{true}, nil
	case "stats.top", "stats.rank", "stats.around":
		b, err := boardArg(d, name, arg(0))
		if err != nil {
			return nil, err
		}
		n, names, limit := 2, []string{"server", "period", "limit"}, 10
		var steam string
		if name != "stats.top" {
			if steam, err = steamID(name, 2, arg(1)); err != nil {
				return nil, err
			}
			n, limit = 3, 5
			if name == "stats.rank" {
				names = names[:2]
			}
		}
		opts, err := options(name, n, arg(n-1), names...)
		if err != nil {
			return nil, err
		}
		var server string
		if v := opts["server"]; v != nil {
			s, ok := v.(string)
			if ok = ok && r.m.Servers != nil; ok {
				_, ok = r.m.Servers.Get(s)
			}
			if !ok {
				return nil, fmt.Errorf("bad argument #%d to '%s' (no server %v)", n, name, v)
			}
			server = s
		}
		period, ok := opts["period"].(string)
		if opts["period"] == nil {
			period, ok = "all", true
		}
		if !ok || !slices.Contains(store.Periods, period) {
			return nil, fmt.Errorf("bad argument #%d to '%s' (period is \"all\", \"month\" or \"week\")", n, name)
		}
		if limit, err = limitArg(name, n, opts["limit"], limit); err != nil {
			return nil, err
		}
		rk, err := d.ranking(b, server, period)
		if err != nil {
			return []any{nil, busy(err)}, nil
		}
		i, on := rk.pos[steam]
		switch {
		case name == "stats.top":
			return []any{rows(rk.list[:min(limit, len(rk.list))])}, nil
		case name == "stats.rank" && on:
			return []any{rk.list[i].Rank, rk.list[i].Value}, nil
		case name == "stats.rank":
			return []any{nil}, nil
		case !on:
			return []any{[]any{}}, nil
		}
		from := max(0, min(i-(limit-1)/2, len(rk.list)-limit))
		return []any{rows(rk.list[from:min(from+limit, len(rk.list))])}, nil
	}
	return nil, fmt.Errorf("no function %s", name)
}

func boardArg(d *data, call string, v any) (store.Board, error) {
	id, ok := v.(string)
	if !ok {
		return store.Board{}, badArg(1, call, "board ID", v)
	}
	b, ok := d.board(id)
	if !ok {
		return store.Board{}, fmt.Errorf("bad argument #1 to '%s' (no board %q)", call, id)
	}
	return b, nil
}

// options is a table of named options, refusing any but names.
func options(call string, n int, v any, names ...string) (map[string]any, error) {
	out := map[string]any{}
	if v == nil {
		return out, nil
	}
	t, ok := v.(*luavm.Table)
	if !ok {
		return nil, badArg(n, call, "table", v)
	}
	for i, k := range t.Keys {
		s, ok := k.(string)
		if !ok || !slices.Contains(names, s) {
			return nil, fmt.Errorf("bad argument #%d to '%s' (no option %v; it takes %s)", n, call, k, strings.Join(names, ", "))
		}
		out[s] = t.Vals[i]
	}
	return out, nil
}

func limitArg(call string, n int, v any, def int) (int, error) {
	if v == nil {
		return def, nil
	}
	f, ok := number(v)
	if !ok || f < 1 || f > maxRows || f != math.Trunc(f) {
		return 0, fmt.Errorf("bad argument #%d to '%s' (a limit from 1 to %d)", n, call, maxRows)
	}
	return int(f), nil
}

// rows are a board's players as a plugin reads them.
func rows(list []store.Ranked) []any {
	out := make([]any, len(list))
	for i, x := range list {
		out[i] = map[string]any{"rank": x.Rank, "id": x.SteamID, "name": x.Name, "value": x.Value}
	}
	return out
}

// busy is why a read failed, for the plugin.
func busy(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		err = errBusy
	}
	return err.Error()
}
