package events

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

func received(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

func TestNotifyReachesSubscribersOnlyOnCommit(t *testing.T) {
	pool := pgtest.Pool(t)
	hub := NewHub(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	run, job, other := uuid.New(), uuid.New(), uuid.New()
	runCh, unsubRun := hub.Subscribe(RunKey(run))
	defer unsubRun()
	jobCh, unsubJob := hub.Subscribe(JobKey(job))
	defer unsubJob()
	otherCh, unsubOther := hub.Subscribe(RunKey(other))
	defer unsubOther()
	// The listener signals everyone once when it (re)connects; drain that.
	require.True(t, received(runCh))
	require.True(t, received(jobCh))
	require.True(t, received(otherCh))

	// A rolled-back transaction delivers nothing.
	_ = database.InTx(ctx, pool, func(tx pgx.Tx) error {
		require.NoError(t, Notify(ctx, tx, run, job))
		return assert.AnError
	})
	select {
	case <-runCh:
		t.Fatal("notification from a rolled-back transaction")
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, database.InTx(ctx, pool, func(tx pgx.Tx) error { return Notify(ctx, tx, run, job) }))
	assert.True(t, received(runCh))
	assert.True(t, received(jobCh))
	select {
	case <-otherCh:
		t.Fatal("unrelated subscriber was signaled")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSignalsCoalesce(t *testing.T) {
	hub := NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	run := uuid.New()
	ch, unsub := hub.Subscribe(RunKey(run))
	for range 5 {
		hub.Dispatch(`{"r":"` + run.String() + `"}`)
	}
	assert.Len(t, ch, 1, "a slow reader sees one pending signal")
	unsub()
	hub.Dispatch(`{"r":"` + run.String() + `"}`)
	hub.Dispatch(`not json`)
	assert.Empty(t, hub.subs)
}
