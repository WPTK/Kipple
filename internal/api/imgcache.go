package api

import (
	"context"
	"net/http"

	"github.com/WPTK/kipple/internal/store"
)

// imgcacheView is GET /api/imgcache. Times are unix seconds; oldest_access_at is
// null when the cache is empty.
type imgcacheView struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"`
	CacheMB        int    `json:"cache_mb"`
	MaxBytes       int64  `json:"max_bytes"`
	UsedBytes      int64  `json:"used_bytes"`
	Entries        int64  `json:"entries"`
	NegEntries     int64  `json:"neg_entries"`
	Thumbnails     int64  `json:"thumbnails"` // cached card thumbnails, a subset of entries
	Hits           int64  `json:"hits"`
	Misses         int64  `json:"misses"`
	Evictions      int64  `json:"evictions"`
	Failures       int64  `json:"failures"`
	Since          int64  `json:"since"`
	OldestAccessAt *int64 `json:"oldest_access_at"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	DiskFloorBytes uint64 `json:"disk_floor_bytes"`
	LowDisk        bool   `json:"low_disk"`
}

// imgcacheStats is GET /api/imgcache: the cache's size and effectiveness, the
// cap, and the free space on the data volume.
func (s *Server) imgcacheStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v := imgcacheView{
		Mode:    s.db.StringSetting(ctx, "imgproxy.mode", store.DefaultImgMode),
		CacheMB: s.db.IntSetting(ctx, "imgproxy.cache_mb", store.DefaultImgCacheMB),
	}
	if c := s.opt.ImgCache; c != nil {
		st := c.Stats()
		v.Enabled = st.Enabled
		v.MaxBytes, v.UsedBytes, v.Entries, v.NegEntries = st.MaxBytes, st.UsedBytes, st.Files, st.NegEntries
		v.Thumbnails = st.Thumbnails
		v.Hits, v.Misses, v.Evictions, v.Failures = st.Hits, st.Misses, st.Evictions, st.Failures
		v.Since = st.Since.Unix()
		if !st.OldestAccess.IsZero() {
			t := st.OldestAccess.Unix()
			v.OldestAccessAt = &t
		}
		v.DiskFreeBytes, v.DiskFloorBytes, v.LowDisk = st.DiskFree, st.DiskFloor, st.LowDisk
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// imgcacheClear is POST /api/imgcache/clear: every cached image, remembered
// failure and hotlink hint is deleted. Images are simply fetched again when next shown.
func (s *Server) imgcacheClear(w http.ResponseWriter, r *http.Request) {
	var n int64
	if c := s.opt.ImgCache; c != nil {
		var err error
		if n, err = c.Clear(); err != nil {
			s.serverError(w, "imgcache clear", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

// diskUsage is the health view's disk footprint. The image cache reports its
// own size (its byte counter plus the index files) rather than the store
// walking and stat-ing every cached file on each health call.
func (s *Server) diskUsage() store.DiskUsage {
	c := s.opt.ImgCache
	return s.db.DiskUsageWith(func() int64 {
		if c == nil {
			return 0
		}
		return c.DiskBytes()
	})
}

// applyImgCacheCap pushes the imgproxy.cache_mb setting into the cache: a lower
// cap evicts down to 90% of it, 0 turns the cache off and purges it.
func (s *Server) applyImgCacheCap(ctx context.Context) {
	if c := s.opt.ImgCache; c != nil {
		mb := s.db.IntSetting(ctx, "imgproxy.cache_mb", store.DefaultImgCacheMB)
		c.SetCap(int64(mb) << 20)
	}
}
