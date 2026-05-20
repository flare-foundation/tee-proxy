// Package init is a test-only re-export of internal/proxy.Run; do not import from production code.
package init

import (
	"context"

	"github.com/flare-foundation/tee-proxy/internal/proxy"
)

// Init boots the proxy from the given config path (test-only).
func Init(ctx context.Context, cfgPath string) {
	proxy.Run(ctx, cfgPath)
}
