//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/modelavailability"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestModelAvailabilityAtomicAcrossInstances(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			model := t.Name()
			t.Cleanup(func() {
				_, _ = integrationEntClient.ModelAvailability.Delete().Where(modelavailability.ModelID(model)).Exec(context.Background())
			})
			if existing {
				require.NoError(t, NewModelAvailabilityRepository(integrationEntClient).Upsert(ctx, "openai", model, func(s service.AvailabilityState) service.AvailabilityState { return s }))
			}
			const n = 12
			start := make(chan struct{})
			errs := make(chan error, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					repo := NewModelAvailabilityRepository(integrationEntClient)
					errs <- repo.Upsert(ctx, "openai", model, func(s service.AvailabilityState) service.AvailabilityState {
						time.Sleep(10 * time.Millisecond) // Overlap independent writers after their reads.
						s.SampleTotal24h++
						s.SampleOK24h++
						return s
					})
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			state, err := NewModelAvailabilityRepository(integrationEntClient).Get(ctx, "openai", model)
			require.NoError(t, err)
			require.Equal(t, n, state.SampleTotal24h)
			require.Equal(t, n, state.SampleOK24h)
		})
	}
}

func TestModelAvailabilitySingleConnectionAndRollback(t *testing.T) {
	db, err := sql.Open("postgres", integrationPostgresDSN)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer client.Close()
	repo := NewModelAvailabilityRepository(client)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	model := t.Name()
	t.Cleanup(func() {
		_, _ = integrationEntClient.ModelAvailability.Delete().Where(modelavailability.ModelID(model)).Exec(context.Background())
	})
	require.NoError(t, repo.Upsert(ctx, "openai", model, func(s service.AvailabilityState) service.AvailabilityState { s.SampleTotal24h = 1; return s }))
	require.Panics(t, func() {
		_ = repo.Upsert(ctx, "openai", model, func(service.AvailabilityState) service.AvailabilityState { panic("callback failed") })
	})
	state, err := repo.Get(ctx, "openai", model)
	require.NoError(t, err)
	require.Equal(t, 1, state.SampleTotal24h)
	require.Equal(t, 0, db.Stats().InUse, "transaction must release its sole connection after panic")
}

func TestModelAvailabilityContendedWriteDeadline(t *testing.T) {
	model := t.Name()
	t.Cleanup(func() {
		_, _ = integrationEntClient.ModelAvailability.Delete().Where(modelavailability.ModelID(model)).Exec(context.Background())
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo := NewModelAvailabilityRepository(integrationEntClient)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- repo.Upsert(ctx, "openai", model, func(s service.AvailabilityState) service.AvailabilityState {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			s.SampleTotal24h++
			return s
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer waitCancel()
	started := time.Now()
	err := NewModelAvailabilityRepository(integrationEntClient).Upsert(waitCtx, "openai", model, func(s service.AvailabilityState) service.AvailabilityState {
		t.Error("expired waiter must not run callback")
		return s
	})
	close(release)
	require.Error(t, err)
	require.ErrorIs(t, waitCtx.Err(), context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	require.NoError(t, <-done)
	state, err := repo.Get(ctx, "openai", model)
	require.NoError(t, err)
	require.Equal(t, 1, state.SampleTotal24h)
}
