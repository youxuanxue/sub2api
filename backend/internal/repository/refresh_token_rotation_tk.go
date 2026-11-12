package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var claimRefreshRotationScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return nil end
local data = cjson.decode(value)
if data.rotated_at then return {0, value} end
-- Preserve int64 IDs/token versions: cjson numbers are IEEE doubles.
local rotated = string.sub(value, 1, -2) .. ',"rotated_at":' .. cjson.encode(ARGV[1]) .. ',"rotation_claim":' .. cjson.encode(ARGV[2]) .. '}'
local ttl = math.min(redis.call('PTTL', KEYS[1]), tonumber(ARGV[3]))
if ttl <= 0 then return nil end
redis.call('SET', KEYS[1], rotated, 'PX', ttl)
return {1, rotated}
`)

var completeRefreshRotationScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return 0 end
local data = cjson.decode(value)
if data.rotation_claim ~= ARGV[1] or data.rotation_result then return 0 end
if redis.call('SISMEMBER', KEYS[3], ARGV[5]) ~= 1 or redis.call('SISMEMBER', KEYS[4], ARGV[5]) ~= 1 then return 0 end
local remaining = redis.call('PTTL', KEYS[1])
if remaining <= 0 then return 0 end
local completed = string.sub(value, 1, -2) .. ',"rotation_result":' .. cjson.encode(ARGV[3]) .. '}'
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[4])
redis.call('SADD', KEYS[3], ARGV[6])
redis.call('SADD', KEYS[4], ARGV[6])
redis.call('PEXPIRE', KEYS[3], ARGV[4])
redis.call('PEXPIRE', KEYS[4], ARGV[4])
redis.call('SET', KEYS[1], completed, 'PX', remaining)
return 1
`)

func (c *refreshTokenCache) ClaimRefreshToken(ctx context.Context, hash, claim string, grace time.Duration) (*service.RefreshTokenData, bool, error) {
	result, err := claimRefreshRotationScript.Run(ctx, c.rdb, []string{refreshTokenKey(hash)}, time.Now().UTC().Format(time.RFC3339Nano), claim, grace.Milliseconds()).Slice()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, service.ErrRefreshTokenNotFound
		}
		return nil, false, err
	}
	var data service.RefreshTokenData
	if len(result) != 2 {
		return nil, false, fmt.Errorf("invalid refresh rotation claim result")
	}
	owned, ok := result[0].(int64)
	encoded, encodedOK := result[1].(string)
	if !ok || !encodedOK || (owned != 0 && owned != 1) {
		return nil, false, fmt.Errorf("invalid refresh rotation claim result types")
	}
	if err := json.Unmarshal([]byte(encoded), &data); err != nil {
		return nil, false, err
	}
	return &data, owned == 1, nil
}

func (c *refreshTokenCache) CompleteRefreshTokenRotation(ctx context.Context, hash, claim, successorHash string, successor *service.RefreshTokenData, encryptedResult string, ttl time.Duration) (bool, error) {
	encoded, err := json.Marshal(successor)
	if err != nil {
		return false, err
	}
	result, err := completeRefreshRotationScript.Run(ctx, c.rdb, []string{refreshTokenKey(hash), refreshTokenKey(successorHash), userRefreshTokensKey(successor.UserID), tokenFamilyKey(successor.FamilyID)}, claim, encoded, encryptedResult, ttl.Milliseconds(), hash, successorHash).Int()
	return result == 1, err
}

// Revocation and successor publication serialize in Redis. A pipeline with an
// earlier SMEMBERS read can miss a successor published between read and DEL.
var revokeRefreshTokenSetScript = redis.NewScript(`
local hashes = redis.call('SMEMBERS', KEYS[1])
for _, hash in ipairs(hashes) do
    redis.call('DEL', ARGV[1] .. hash)
end
redis.call('DEL', KEYS[1])
return #hashes
`)
