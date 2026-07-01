package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/ent"
	enttier "github.com/Wei-Shaw/sub2api/ent/tier"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type tierRepository struct {
	client *ent.Client
}

// NewTierRepository 创建 tier（anthropic OAuth 稳定性档位）仓库。
func NewTierRepository(client *ent.Client) service.TierRepository {
	return &tierRepository{client: client}
}

// List 获取所有 tier（按 name 升序，l1..l5）。
func (r *tierRepository) List(ctx context.Context) ([]*model.Tier, error) {
	rows, err := r.client.Tier.Query().Order(ent.Asc(enttier.FieldName)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Tier, len(rows))
	for i, e := range rows {
		out[i] = r.toModel(e)
	}
	return out, nil
}

// GetByID 根据 ID 获取 tier；不存在返回 nil。
func (r *tierRepository) GetByID(ctx context.Context, id int64) (*model.Tier, error) {
	e, err := r.client.Tier.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.toModel(e), nil
}

// GetByName 根据 name（l1..l5）获取 tier；不存在返回 nil。
func (r *tierRepository) GetByName(ctx context.Context, name string) (*model.Tier, error) {
	e, err := r.client.Tier.Query().Where(enttier.NameEQ(name)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.toModel(e), nil
}

// Create 创建 tier。
func (r *tierRepository) Create(ctx context.Context, t *model.Tier) (*model.Tier, error) {
	created, err := r.applyMutation(r.client.Tier.Create(), t).Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(created), nil
}

// Update 更新 tier。
func (r *tierRepository) Update(ctx context.Context, t *model.Tier) (*model.Tier, error) {
	b := r.client.Tier.UpdateOneID(t.ID).
		SetName(t.Name).
		SetConcurrency(t.Concurrency).
		SetPriority(t.Priority).
		SetRateMultiplier(t.RateMultiplier).
		SetBaseRpm(t.BaseRPM).
		SetMaxSessions(t.MaxSessions).
		SetRpmStickyBuffer(t.RPMStickyBuffer).
		SetSessionIdleTimeoutMinutes(t.SessionIdleTimeoutMinutes).
		SetCacheTTLOverrideEnabled(t.CacheTTLOverrideEnabled)
	applyTierNillable(b, t)
	updated, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(updated), nil
}

// Delete 删除 tier。
func (r *tierRepository) Delete(ctx context.Context, id int64) error {
	return r.client.Tier.DeleteOneID(id).Exec(ctx)
}

// UpsertByName 按 name upsert（startup ensureSeededFromBaseline 用）：存在则更新
// 全部列、不存在则创建。返回结果模型。
func (r *tierRepository) UpsertByName(ctx context.Context, t *model.Tier) (*model.Tier, error) {
	existing, err := r.GetByName(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		t.ID = existing.ID
		// 保留 DB 已有的 tls_profile_id（流水线/apply 维护），seed 不覆盖为 nil。
		if t.TLSProfileID == nil {
			t.TLSProfileID = existing.TLSProfileID
		}
		return r.Update(ctx, t)
	}
	return r.Create(ctx, t)
}

func (r *tierRepository) applyMutation(b *ent.TierCreate, t *model.Tier) *ent.TierCreate {
	b = b.SetName(t.Name).
		SetConcurrency(t.Concurrency).
		SetPriority(t.Priority).
		SetRateMultiplier(t.RateMultiplier).
		SetBaseRpm(t.BaseRPM).
		SetMaxSessions(t.MaxSessions).
		SetRpmStickyBuffer(t.RPMStickyBuffer).
		SetSessionIdleTimeoutMinutes(t.SessionIdleTimeoutMinutes).
		SetCacheTTLOverrideEnabled(t.CacheTTLOverrideEnabled)
	if t.Description != nil {
		b = b.SetDescription(*t.Description)
	}
	if t.CacheTTLOverrideTarget != nil {
		b = b.SetCacheTTLOverrideTarget(*t.CacheTTLOverrideTarget)
	}
	if t.TLSProfileName != nil {
		b = b.SetTLSProfileName(*t.TLSProfileName)
	}
	if t.TLSProfileID != nil {
		b = b.SetTLSProfileID(*t.TLSProfileID)
	}
	return b
}

func applyTierNillable(b *ent.TierUpdateOne, t *model.Tier) {
	if t.Description != nil {
		b.SetDescription(*t.Description)
	} else {
		b.ClearDescription()
	}
	if t.CacheTTLOverrideTarget != nil {
		b.SetCacheTTLOverrideTarget(*t.CacheTTLOverrideTarget)
	} else {
		b.ClearCacheTTLOverrideTarget()
	}
	if t.TLSProfileName != nil {
		b.SetTLSProfileName(*t.TLSProfileName)
	} else {
		b.ClearTLSProfileName()
	}
	if t.TLSProfileID != nil {
		b.SetTLSProfileID(*t.TLSProfileID)
	} else {
		b.ClearTLSProfileID()
	}
}

func (r *tierRepository) toModel(e *ent.Tier) *model.Tier {
	return &model.Tier{
		ID:                        e.ID,
		Name:                      e.Name,
		Description:               e.Description,
		Concurrency:               e.Concurrency,
		Priority:                  e.Priority,
		RateMultiplier:            e.RateMultiplier,
		BaseRPM:                   e.BaseRpm,
		MaxSessions:               e.MaxSessions,
		RPMStickyBuffer:           e.RpmStickyBuffer,
		SessionIdleTimeoutMinutes: e.SessionIdleTimeoutMinutes,
		CacheTTLOverrideEnabled:   e.CacheTTLOverrideEnabled,
		CacheTTLOverrideTarget:    e.CacheTTLOverrideTarget,
		TLSProfileName:            e.TLSProfileName,
		TLSProfileID:              e.TLSProfileID,
		CreatedAt:                 e.CreatedAt,
		UpdatedAt:                 e.UpdatedAt,
	}
}
