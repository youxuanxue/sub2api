package repository

import (
	"context"
	"database/sql"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/modelavailability"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// modelAvailabilityRepository implements service.ModelAvailabilityRepository.
//
// The production PostgreSQL row lock serializes state transitions across app
// instances. Lock acquisition and all reads/writes share the caller's deadline.
type modelAvailabilityRepository struct {
	client *dbent.Client
}

// NewModelAvailabilityRepository constructs the ent-backed repository.
func NewModelAvailabilityRepository(client *dbent.Client) service.ModelAvailabilityRepository {
	return &modelAvailabilityRepository{client: client}
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

func (r *modelAvailabilityRepository) GetBatch(ctx context.Context, platform string, modelIDs []string) (map[string]service.AvailabilityState, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("model availability repo: nil client")
	}
	if len(modelIDs) == 0 {
		return map[string]service.AvailabilityState{}, nil
	}
	rows, err := r.client.ModelAvailability.Query().
		Where(modelavailability.PlatformEQ(modelavailability.Platform(platform))).
		Where(modelavailability.ModelIDIn(modelIDs...)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]service.AvailabilityState, len(rows))
	for _, row := range rows {
		state := entRowToState(row)
		out[state.ModelID] = state
	}
	return out, nil
}

func (r *modelAvailabilityRepository) Upsert(ctx context.Context, platform, modelID string, fn func(service.AvailabilityState) service.AvailabilityState) error {
	if r == nil || r.client == nil {
		return errors.New("model availability repo: nil client")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	// Also release the connection if the state callback panics.
	defer func() { _ = tx.Rollback() }()
	plat := modelavailability.Platform(platform)
	// Materialize an empty cell before locking it, so competing first writers
	// serialize on the same unique key without losing an observation.
	err = tx.ModelAvailability.Create().SetPlatform(plat).SetModelID(modelID).
		OnConflictColumns(modelavailability.FieldPlatform, modelavailability.FieldModelID).
		DoNothing().Exec(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	row, err := tx.ModelAvailability.Query().
		Where(modelavailability.PlatformEQ(plat), modelavailability.ModelID(modelID)).
		ForUpdate().Only(ctx)
	if err != nil {
		return err
	}
	cur := entRowToState(row)
	next := fn(cur)
	next.Platform = platform
	next.ModelID = modelID

	status := modelavailability.Status(orDefault(next.Status, "untested"))
	// Update existing row.
	u := tx.ModelAvailability.Update().
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
	if err != nil {
		return err
	}
	return tx.Commit()
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
		LastFailureAt:          row.LastFailureAt,          // already *time.Time
		LastCheckedAt:          row.LastCheckedAt,          // already *time.Time
		UpstreamStatusCodeLast: row.UpstreamStatusCodeLast, // already *int
		RollingWindowStartedAt: row.RollingWindowStartedAt, // already *time.Time
		LastAccountID:          row.LastAccountID,          // already *int64
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
