package repository

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const defaultSettingCacheTTL = 5 * time.Second

type settingCacheEntry struct {
	setting *service.Setting
	expires time.Time
}

type settingRepository struct {
	client    *ent.Client
	mu        sync.RWMutex
	cache     map[string]settingCacheEntry
	allCached bool
	allExpire time.Time
	ttl       time.Duration
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{
		client: client,
		cache:  make(map[string]settingCacheEntry),
		ttl:    defaultSettingCacheTTL,
	}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	now := time.Now()
	r.mu.RLock()
	if entry, ok := r.cache[key]; ok && now.Before(entry.expires) {
		r.mu.RUnlock()
		if entry.setting == nil {
			return nil, service.ErrSettingNotFound
		}
		cp := *entry.setting
		return &cp, nil
	}
	r.mu.RUnlock()

	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			r.mu.Lock()
			r.cache[key] = settingCacheEntry{setting: nil, expires: time.Now().Add(r.ttl)}
			r.mu.Unlock()
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	val := &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}
	r.mu.Lock()
	r.cache[key] = settingCacheEntry{setting: val, expires: time.Now().Add(r.ttl)}
	r.mu.Unlock()

	cp := *val
	return &cp, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	err := r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.cache[key] = settingCacheEntry{
		setting: &service.Setting{Key: key, Value: value, UpdatedAt: now},
		expires: now.Add(r.ttl),
	}
	r.allCached = false
	r.mu.Unlock()
	return nil
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}

	now := time.Now()
	r.mu.RLock()
	result := make(map[string]string, len(keys))
	var missing []string
	for _, k := range keys {
		if entry, ok := r.cache[k]; ok && now.Before(entry.expires) {
			if entry.setting != nil {
				result[k] = entry.setting.Value
			}
		} else {
			missing = append(missing, k)
		}
	}
	r.mu.RUnlock()

	if len(missing) == 0 {
		return result, nil
	}

	settings, err := r.client.Setting.Query().Where(setting.KeyIn(missing...)).All(ctx)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	now = time.Now()
	found := make(map[string]bool, len(settings))
	for _, s := range settings {
		found[s.Key] = true
		val := &service.Setting{
			ID:        s.ID,
			Key:       s.Key,
			Value:     s.Value,
			UpdatedAt: s.UpdatedAt,
		}
		r.cache[s.Key] = settingCacheEntry{setting: val, expires: now.Add(r.ttl)}
		result[s.Key] = s.Value
	}
	for _, m := range missing {
		if !found[m] {
			r.cache[m] = settingCacheEntry{setting: nil, expires: now.Add(r.ttl)}
		}
	}
	r.mu.Unlock()

	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	err := r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return err
	}

	r.mu.Lock()
	for key, value := range settings {
		r.cache[key] = settingCacheEntry{
			setting: &service.Setting{Key: key, Value: value, UpdatedAt: now},
			expires: now.Add(r.ttl),
		}
	}
	r.allCached = false
	r.mu.Unlock()
	return nil
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	now := time.Now()
	r.mu.RLock()
	if r.allCached && now.Before(r.allExpire) {
		result := make(map[string]string, len(r.cache))
		for k, v := range r.cache {
			if v.setting != nil {
				result[k] = v.setting.Value
			}
		}
		r.mu.RUnlock()
		return result, nil
	}
	r.mu.RUnlock()

	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	now = time.Now()
	r.cache = make(map[string]settingCacheEntry, len(settings))
	result := make(map[string]string, len(settings))
	for _, s := range settings {
		val := &service.Setting{
			ID:        s.ID,
			Key:       s.Key,
			Value:     s.Value,
			UpdatedAt: s.UpdatedAt,
		}
		r.cache[s.Key] = settingCacheEntry{setting: val, expires: now.Add(r.ttl)}
		result[s.Key] = s.Value
	}
	r.allCached = true
	r.allExpire = now.Add(r.ttl)
	r.mu.Unlock()

	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	if err != nil {
		return err
	}

	r.mu.Lock()
	delete(r.cache, key)
	r.allCached = false
	r.mu.Unlock()
	return nil
}
