package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type edgeAdminHandoffCache struct{ rdb *redis.Client }

func NewEdgeAdminHandoffCache(rdb *redis.Client) service.EdgeHandoffCache {
	return &edgeAdminHandoffCache{rdb: rdb}
}

var edgeHandoffCreateScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 or redis.call('EXISTS', KEYS[2]) == 1 then return 0 end
redis.call('SET', KEYS[1], '1', 'PX', ARGV[2])
redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
return 1
`)
var edgeHandoffConsumeScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return '' end
local row = cjson.decode(raw)
if row.attempt ~= ARGV[1] or row.challenge ~= ARGV[2] then return '' end
redis.call('DEL', KEYS[1])
return raw
`)

func (c *edgeAdminHandoffCache) Create(ctx context.Context, attempt, code string, claims service.EdgeHandoffClaims, ttl time.Duration) error {
	if ttl <= 0 {
		return service.ErrEdgeHandoffInvalid
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	n, err := edgeHandoffCreateScript.Run(ctx, c.rdb, []string{"{edge-handoff}:attempt:" + attempt, "{edge-handoff}:code:" + code}, string(raw), ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrEdgeHandoffInvalid
	}
	return nil
}
func (c *edgeAdminHandoffCache) Consume(ctx context.Context, code, attempt, challenge string) (*service.EdgeHandoffClaims, error) {
	raw, err := edgeHandoffConsumeScript.Run(ctx, c.rdb, []string{"{edge-handoff}:code:" + code}, attempt, challenge).Text()
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, service.ErrEdgeHandoffInvalid
	}
	var claims service.EdgeHandoffClaims
	if err := json.Unmarshal([]byte(raw), &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}
