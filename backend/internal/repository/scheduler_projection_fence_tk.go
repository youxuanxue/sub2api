package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const schedulerProjectionGenerationKey = "sched:outbox:projection_generation"

type schedulerProjectionContextKey struct{}

var errSchedulerProjectionFenced = errors.New("scheduler projection generation superseded")

var _ service.SchedulerOutboxProjectionFencer = (*schedulerCache)(nil)

var beginSchedulerProjectionScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
-- Decimal string comparison avoids Lua's floating-point integer limit.
if current and (#current > #ARGV[1] or (#current == #ARGV[1] and current >= ARGV[1])) then
    return 0
end
redis.call('SET', KEYS[1], ARGV[1])
return 1
`)

func (c *schedulerCache) BeginOutboxProjection(ctx context.Context, generation int64) (context.Context, error) {
	if generation <= 0 {
		return nil, fmt.Errorf("invalid projection generation")
	}
	token := strconv.FormatInt(generation, 10)
	ok, err := beginSchedulerProjectionScript.Run(ctx, c.rdb, []string{schedulerProjectionGenerationKey}, token).Int64()
	if err != nil {
		return nil, err
	}
	if ok != 1 {
		return nil, errSchedulerProjectionFenced
	}
	return context.WithValue(ctx, schedulerProjectionContextKey{}, token), nil
}

// WATCH and EXEC bind the generation check and all writes to one Redis
// connection. A new owner invalidates even an already queued old transaction.
func (c *schedulerCache) execSchedulerPipeline(ctx context.Context, pipe redis.Pipeliner) ([]redis.Cmder, error) {
	token, guarded := ctx.Value(schedulerProjectionContextKey{}).(string)
	if !guarded {
		return pipe.Exec(ctx)
	}
	commands := append([]redis.Cmder(nil), pipe.Cmds()...)
	pipe.Discard()
	err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
		current, err := tx.Get(ctx, schedulerProjectionGenerationKey).Result()
		if errors.Is(err, redis.Nil) || (err == nil && current != token) {
			return errSchedulerProjectionFenced
		}
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
			for _, cmd := range commands {
				if err := p.Process(ctx, cmd); err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, redis.TxFailedErr) {
			return errSchedulerProjectionFenced
		}
		return err
	}, schedulerProjectionGenerationKey)
	return commands, err
}

func (c *schedulerCache) runSchedulerScript(ctx context.Context, script *redis.Script, keys []string, args ...any) *redis.Cmd {
	if _, guarded := ctx.Value(schedulerProjectionContextKey{}).(string); !guarded {
		return script.Run(ctx, c.rdb, keys, args...)
	}
	pipe := c.rdb.Pipeline()
	cmd := script.Eval(ctx, pipe, keys, args...)
	if _, err := c.execSchedulerPipeline(ctx, pipe); err != nil {
		cmd.SetErr(err)
	}
	return cmd
}
