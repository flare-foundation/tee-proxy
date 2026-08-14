package testutil

import (
	"context"
	"strings"

	"github.com/redis/go-redis/v9"
)

// AfterCommand returns a hook that runs f once a command with the given name has executed,
// so a test can act at an exact point of a Redis sequence.
func AfterCommand(name string, f func()) redis.Hook {
	return &afterHook{name: name, f: f}
}

type afterHook struct {
	name string
	f    func()
}

var _ redis.Hook = &afterHook{}

func (*afterHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *afterHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if strings.EqualFold(cmd.Name(), h.name) {
			h.f()
		}
		return err
	}
}

func (*afterHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
