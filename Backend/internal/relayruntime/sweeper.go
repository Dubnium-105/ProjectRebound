package relayruntime

import (
	"context"
	"time"
)

func (r *Runtime) RunSweeper(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Sweep()
		}
	}
}
