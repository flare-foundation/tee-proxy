package init

import (
	"context"

	"github.com/flare-foundation/tee-proxy/internal/proxy"
)

// Init is only used for testing purposes.
func Init(ctx context.Context, cfgPath string) {
	proxy.Run(ctx, cfgPath)
}
