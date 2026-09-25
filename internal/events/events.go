// Package events delivers "something changed" signals from database transactions to
// long-lived HTTP streams (SSE). Writers call Notify inside their transaction, which runs
// pg_notify: PostgreSQL delivers it only on commit, so listeners never see uncommitted
// state, and every API replica receives it. Each process runs one Hub that LISTENs on a
// dedicated connection and fans signals out to subscribers keyed by resource.
//
// Signals carry no data: subscribers re-read what they need, with their own authorization.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the PostgreSQL notification channel.
const Channel = "opshub_events"

// Execer is satisfied by pgx.Tx, *pgxpool.Pool and *pgx.Conn.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type message struct {
	Run        uuid.UUID `json:"r,omitempty"`
	Job        uuid.UUID `json:"j,omitempty"`
	Deployment uuid.UUID `json:"d,omitempty"`
}

// RunKey and JobKey name subscription topics.
func RunKey(id uuid.UUID) string { return "run:" + id.String() }
func JobKey(id uuid.UUID) string { return "job:" + id.String() }

// DeploymentKey names a deployment's topic.
func DeploymentKey(id uuid.UUID) string { return "deployment:" + id.String() }

// Notify signals that a run (and optionally one of its jobs) changed. Call it inside the
// transaction that made the change.
func Notify(ctx context.Context, db Execer, runID, jobID uuid.UUID) error {
	b, err := json.Marshal(message{Run: runID, Job: jobID})
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(b))
	return err
}

// NotifyDeployment signals that a deployment (status or logs) changed.
func NotifyDeployment(ctx context.Context, db Execer, deploymentID uuid.UUID) error {
	b, err := json.Marshal(message{Deployment: deploymentID})
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(b))
	return err
}

// Hub fans notifications out to subscribers in this process.
type Hub struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewHub(pool *pgxpool.Pool, logger *slog.Logger) *Hub {
	return &Hub{pool: pool, logger: logger, subs: map[string]map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that receives a value whenever key changes. Signals coalesce:
// a slow reader sees one pending signal, not a backlog. Call cancel when done.
func (h *Hub) Subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[key] == nil {
		h.subs[key] = map[chan struct{}]struct{}{}
	}
	h.subs[key][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[key], ch)
		if len(h.subs[key]) == 0 {
			delete(h.subs, key)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) publish(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[key] {
		select {
		case ch <- struct{}{}:
		default: // already signaled
		}
	}
}

// Dispatch delivers a raw notification payload (used by Run, and directly by tests).
func (h *Hub) Dispatch(payload string) {
	var m message
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return
	}
	if m.Run != uuid.Nil {
		h.publish(RunKey(m.Run))
	}
	if m.Job != uuid.Nil {
		h.publish(JobKey(m.Job))
	}
	if m.Deployment != uuid.Nil {
		h.publish(DeploymentKey(m.Deployment))
	}
}

// Run listens until ctx is done, reconnecting with backoff. After a reconnect every
// subscriber is signaled once, since notifications may have been missed.
func (h *Hub) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := h.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		h.logger.WarnContext(ctx, "event listener disconnected; retrying", "error", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (h *Hub) listen(ctx context.Context) error {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	h.signalAll() // anything could have changed while we weren't listening
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			// Don't return a connection in LISTEN state to the pool.
			_ = conn.Conn().Close(context.Background())
			return err
		}
		h.Dispatch(n.Payload)
	}
}

func (h *Hub) signalAll() {
	h.mu.Lock()
	keys := make([]string, 0, len(h.subs))
	for k := range h.subs {
		keys = append(keys, k)
	}
	h.mu.Unlock()
	for _, k := range keys {
		h.publish(k)
	}
}
