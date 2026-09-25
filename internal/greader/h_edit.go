package greader

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/WPTK/kipple/internal/store"
)

func (a *API) registerEdit() {
	a.routes["edit-tag"] = route{h: (*call).editTag, post: true}
	a.routes["mark-all-as-read"] = route{h: (*call).markAllAsRead, post: true}
}

// tagOp is one state change requested by an a= or r= value.
type tagOp struct {
	read    *bool // set read state
	starred *bool // set starred state
}

func boolp(b bool) *bool { return &b }

// tagOps maps a= (add) and r= (remove) tag values to state changes (design §6.7).
// Labels, broadcast, like, tracking-* and unknown tags are ignored.
func tagOps(add, remove []string) []tagOp {
	var ops []tagOp
	for _, v := range add {
		switch n, _ := stateName(v); n {
		case "read":
			ops = append(ops, tagOp{read: boolp(true)})
		case "kept-unread":
			ops = append(ops, tagOp{read: boolp(false)})
		case "starred":
			ops = append(ops, tagOp{starred: boolp(true)})
		}
	}
	for _, v := range remove {
		switch n, _ := stateName(v); n {
		case "read":
			ops = append(ops, tagOp{read: boolp(false)})
		case "kept-unread":
			ops = append(ops, tagOp{read: boolp(true)})
		case "starred":
			ops = append(ops, tagOp{starred: boolp(false)})
		}
	}
	return ops
}

// editTag is POST edit-tag. It is always 200 OK: zero ids, unknown ids, trimmed
// ids and an empty i all succeed (a non-2xx wedges NetNewsWire's queue).
func (c *call) editTag() {
	raw := c.p.All("i")
	if len(raw) > maxEditIDs {
		c.text(http.StatusBadRequest, "Bad Request")
		return
	}
	ids := make([]int64, 0, len(raw))
	for _, v := range raw {
		if id, ok := ParseItemID(v); ok {
			ids = append(ids, id)
		}
	}
	c.warnNoIDs(len(ids))
	ops := tagOps(c.p.All("a"), c.p.All("r"))
	if len(ids) == 0 || len(ops) == 0 {
		c.ok()
		return
	}
	now := c.a.now().Unix()
	type change struct {
		op  tagOp
		res store.StateResult
	}
	var changes []change
	err := c.a.db.WithWrite(c.r.Context(), func(ctx context.Context, tx *sql.Tx) error {
		changes = changes[:0]
		for _, op := range ops {
			var res store.StateResult
			var err error
			switch {
			case op.read != nil:
				res, err = store.SetRead(ctx, tx, ids, *op.read, now)
			case op.starred != nil:
				res, err = store.SetStarred(ctx, tx, ids, *op.starred, now)
			}
			if err != nil {
				return err
			}
			changes = append(changes, change{op, res})
		}
		return nil
	})
	if err != nil {
		c.serverError("edit-tag", err)
		return
	}
	for _, ch := range changes {
		if len(ch.res.Changed) == 0 {
			continue
		}
		ev := map[string]any{"ids": idStrings(ch.res.Changed), "source": c.family}
		if ch.op.read != nil {
			ev["read"] = *ch.op.read
		}
		if ch.op.starred != nil {
			ev["starred"] = *ch.op.starred
		}
		if len(ch.res.Restored) > 0 {
			ev["restored"] = idStrings(ch.res.Restored)
		}
		c.a.publish("items.state", ev)
	}
	c.ok()
}

// maxEditIDs caps the item ids in one edit-tag request.
const maxEditIDs = 10000

func idStrings(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.FormatInt(id, 10)
	}
	return out
}

// normalizeTS converts a mark-all-as-read ts to microseconds by digit count
// (design §3): 1-12 digits seconds, 13-15 milliseconds, 16 microseconds, 17 or
// more nanoseconds. ok is false for absent, zero, non-digit or overflowing
// values, which fall back to the committed max id.
func normalizeTS(s string) (us int64, ok bool) {
	if !allDigits(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	switch l := len(s); {
	case l <= 12:
		return n * 1_000_000, true
	case l <= 15:
		return n * 1_000, true
	case l == 16:
		return n, true
	default:
		return n / 1_000, true
	}
}

// markAllAsRead is POST mark-all-as-read. It never produces stats and is
// always OK; read/unread/broadcast/unknown streams are no-ops.
func (c *call) markAllAsRead() {
	ctx := c.r.Context()
	f, err := c.resolveStream(c.p.Get("s"), firstOrEmpty(c.p.AllRaw("s")))
	if err != nil {
		c.serverError("mark-all-as-read", err)
		return
	}
	if f.Empty || len(f.Read) > 0 {
		c.ok()
		return
	}
	scope := store.MarkScope{FeedID: f.FeedID, FolderID: f.FolderID, Starred: len(f.Starred) > 0}
	ts, ok := normalizeTS(c.p.Get("ts"))
	if !ok {
		if ts, err = c.a.db.MaxCommittedID(ctx); err != nil {
			c.serverError("mark-all-as-read", err)
			return
		}
	}
	now := c.a.now().Unix()
	var n int64
	err = c.a.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		n, err = store.MarkAllRead(ctx, tx, scope, ts, now)
		return err
	})
	if err != nil {
		c.serverError("mark-all-as-read", err)
		return
	}
	if n > 0 {
		c.a.publish("resync", map[string]any{})
	}
	c.ok()
}
