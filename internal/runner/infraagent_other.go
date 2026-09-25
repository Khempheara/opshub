//go:build !linux

package runner

import (
	"context"
	"time"
)

// collect reports no metrics outside Linux: the agent still sends heartbeats, so the server
// shows as online.
func (a *InfraAgent) collect(ctx context.Context) Sample {
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	return Sample{}
}
