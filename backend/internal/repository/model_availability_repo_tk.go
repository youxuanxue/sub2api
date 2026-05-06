package repository

import (
	"context"
	"errors"
	"sync"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/modelavailability"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// modelAvailabilityRepository implements service.ModelAvailabilityRepository.
//
// PR-1 synchronous implementation — one PG round-trip per RecordOutcome.
// Redis write-buffer optimisation is deferred to PR-2 / PR-3 (see
// docs/approved/pricing-availability-source-of-truth.md §2.2).
type modelAvailabilityRepository struct {
	client *dbent.Client

	// per-(platform, model) cell mutex serialises the read-modify-write
	// inside Upsert. The map grows to at most catalog_size × platform_count
	// ≈ 600 entries; memory is negligible.
	cellMu  sync.Mutex
	muCells map[string]*sync.Mutex
}

// NewModelAvailabilityRepository constructs the ent-backed repository.
func NewModelAvailabilityRepository(client *dbent.Client) service.ModelAvailabilityRepository {
	return &modelAvailabilityRepository{
		client:  client,
		muCells: make(map[string]*sync.Mutex),
	}
}

func (r *modelAvailabilityRepository) lockFor(platform, modelID string) *sync.Mutex {
	r.cellMu.Lock()
	defer r.cellMu.Unlock()
	key := platform + "::" + modelID
	mu, ok := r.muCells[key]
	if !ok {
		mu = &sync.Mutex{}
		r.muCells[key] = mu
	}
	return mu
}

func (r *modelAvailabilityRepository) Get(ctx context.Context, platform, modelID string) (service.AvailabilityState, error) {
	if r == nil || r.client == nil {
		return service.AvailabilityState{}, errors.New("model availability repo: nil client")
	}
	row, err := r.client.ModelAvailability.Query().
		Where(modelavailability.PlatformEQ(modelavailability.Platform(platform))).
		Where(modelavailability.ModelID(modelID)).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return service.AvailabilityState{}, nil
	}
	if err != nil {
		return service.AvailabilityState{}, err
	}
	return entRowToState(row), nil
}

func (r *modelAvailabilityRepository) Upsert(ctx context.Context, platform, modelID string, fn func(service.AvailabilityState) service.AvailabilityState) error {
	if r == nil || r.client == nil {
		return errors.New("model availability repo: nil client")
	}
	mu := r.lockFor(platform, modelID)
	mu.Lock()
	defer mu.Unlock()

	cur, err := r.Get(ctx, platform, modelID)
	if err != nil {
		return err
	}
	next := fn(cur)
	next.Platform = platform
	next.ModelID = modelID

	status := modelavailability.Status(orDefault(next.Status, "untested"))
	plat := modelavailability.Platform(platform)

	if cur.Platform == "" {
		// Insert new row.
		c := r.client.ModelAvailability.Create().
			SetPlatform(plat).
			SetModelID(modelID).
			SetStatus(status).
			SetLastFailureKind(next.LastFailureKind).
			SetSampleOk24h(next.SampleOK24h).
			SetSampleTotal24h(next.SampleTotal24h)
		if next.LastSeenOKAt != nil {
			c.SetLastSeenOkAt(*next.LastSeenOKAt)
		}
		if next.LastFailureAt != nil {
			c.SetLastFailureAt(*next.LastFailureAt)
		}
		if next.LastCheckedAt != nil {
			c.SetLastCheckedAt(*next.LastCheckedAt)
		}
		if next.UpstreamStatusCodeLast != nil {
			c.SetUpstreamStatusCodeLast(*next.UpstreamStatusCodeLast)
		}
		if next.RollingWindowStartedAt != nil {
			c.SetRollingWindowStartedAt(*next.RollingWindowStartedAt)
		}
		if next.LastAccountID != nil {
			c.SetLastAccountID(*next.LastAccountID)
		}
		_, err = c.Save(ctx)
		return err
	}

	// Update existing row.
	u := r.client.ModelAvailability.Update().
		Where(modelavailability.PlatformEQ(plat)).
		Where(modelavailability.ModelID(modelID)).
		SetStatus(status).
		SetLastFailureKind(next.LastFailureKind).
		SetSampleOk24h(next.SampleOK24h).
		SetSampleTotal24h(next.SampleTotal24h)
	if next.LastSeenOKAt != nil {
		u.SetLastSeenOkAt(*next.LastSeenOKAt)
	} else {
		u.ClearLastSeenOkAt()
	}
	if next.LastFailureAt != nil {
		u.SetLastFailureAt(*next.LastFailureAt)
	} else {
		u.ClearLastFailureAt()
	}
	if next.LastCheckedAt != nil {
		u.SetLastCheckedAt(*next.LastCheckedAt)
	} else {
		u.ClearLastCheckedAt()
	}
	if next.UpstreamStatusCodeLast != nil {
		u.SetUpstreamStatusCodeLast(*next.UpstreamStatusCodeLast)
	} else {
		u.ClearUpstreamStatusCodeLast()
	}
	if next.RollingWindowStartedAt != nil {
		u.SetRollingWindowStartedAt(*next.RollingWindowStartedAt)
	} else {
		u.ClearRollingWindowStartedAt()
	}
	if next.LastAccountID != nil {
		u.SetLastAccountID(*next.LastAccountID)
	} else {
		u.ClearLastAccountID()
	}
	_, err = u.Save(ctx)
	return err
}

func entRowToState(row *dbent.ModelAvailability) service.AvailabilityState {
	if row == nil {
		return service.AvailabilityState{}
	}
	return service.AvailabilityState{
		Platform:               string(row.Platform),
		ModelID:                row.ModelID,
		Status:                 string(row.Status),
		LastFailureKind:        row.LastFailureKind,
		SampleOK24h:            row.SampleOk24h,
		SampleTotal24h:         row.SampleTotal24h,
		LastSeenOKAt:           row.LastSeenOkAt,           // already *time.Time
		LastFailureAt:          row.LastFailureAt,           // already *time.Time
		LastCheckedAt:          row.LastCheckedAt,           // already *time.Time
		UpstreamStatusCodeLast: row.UpstreamStatusCodeLast, // already *int
		RollingWindowStartedAt: row.RollingWindowStartedAt, // already *time.Time
		LastAccountID:          row.LastAccountID,           // already *int64
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
