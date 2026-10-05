package main

import (
	"context"
	"log/slog"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/reach"
	"github.com/WPTK/kipple/internal/store"
)

// openReach puts the reachability settings in force (design §7.1f): the public
// URL, the allowed host names, the trusted proxies and Cloudflare Access. Their
// environment variables first seed each setting that was never stored (an
// upgraded install keeps what its variables said, a scripted first start gets
// them); a variable whose setting already holds something else is not used, and
// that is logged so a stale line in a compose file is easy to spot. When Access
// is on, its key set is loaded in the background, so a wrong team domain is
// logged at startup rather than on the first sign-in.
func openReach(ctx context.Context, db *store.DB, cfg config.Config, logger *slog.Logger) (*reach.Live, error) {
	ignored, err := reach.SeedSettings(ctx, db, cfg.ReachSeed())
	if err != nil {
		return nil, err
	}
	for _, key := range ignored {
		logger.Warn("an environment variable is not used: Settings already holds another value, and Settings decides (change it under Account & Devices, Address and access, or remove the variable)",
			"variable", reach.EnvNames[key], "setting", key)
	}
	return reach.Open(ctx, db, reach.Options{Logger: logger, Access: access.Options{Logger: logger}, NoPrefetch: !prefetchAccessKeys})
}

// prefetchAccessKeys loads a new Access key set at once (a seam: tests never reach the network).
var prefetchAccessKeys = true
