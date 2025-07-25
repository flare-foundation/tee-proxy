package init

import (
	"context"
	"github.com/flare-foundation/tee-proxy/internal/initialize"
)

// TODO Figure out a place for this
func Init(ctx context.Context, cfgPath string) {
	initialize.Initialize(ctx, cfgPath)
}
