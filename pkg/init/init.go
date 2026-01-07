package init

import (
	"context"

	"github.com/flare-foundation/tee-proxy/internal/initialize"
)

// Init is only used for testing purposes.
func Init(ctx context.Context, cfgPath string) {
	initialize.Initialize(ctx, cfgPath)
}
