package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/telemetryarchive"
)

// usageLogInsertArgTypes must stay in the same order as:
//  1. prepareUsageLogInsert().args
//  2. every INSERT/CTE VALUES column list in this file
//  3. execUsageLogInsertNoResult placeholder positions
//  4. scanUsageLog selected column order (via usageLogSelectColumns)
//
// When adding a usage_logs column, update all of those call sites together.
var usageLogInsertArgTypes = [...]string{
	"bigint",      // user_id
	"bigint",      // api_key_id
	"bigint",      // account_id
	"text",        // request_id
	"text",        // model
	"text",        // requested_model
	"text",        // upstream_model
	"bigint",      // group_id
	"bigint",      // subscription_id
	"integer",     // input_tokens
	"integer",     // output_tokens
	"integer",     // cache_creation_tokens
	"integer",     // cache_read_tokens
	"integer",     // cache_creation_5m_tokens
	"integer",     // cache_creation_1h_tokens
	"integer",     // image_output_tokens
	"numeric",     // image_output_cost
	"integer",     // image_input_tokens
	"numeric",     // image_input_cost
	"numeric",     // input_cost
	"numeric",     // output_cost
	"numeric",     // cache_creation_cost
	"numeric",     // cache_read_cost
	"numeric",     // total_cost
	"numeric",     // actual_cost
	"numeric",     // rate_multiplier
	"numeric",     // account_rate_multiplier
	"smallint",    // billing_type
	"smallint",    // request_type
	"boolean",     // stream
	"boolean",     // openai_ws_mode
	"integer",     // duration_ms
	"integer",     // gateway_latency_ms
	"integer",     // first_token_ms
	"text",        // user_agent
	"text",        // ip_address
	"integer",     // image_count
	"text",        // image_size
	"text",        // image_input_size
	"text",        // image_output_size
	"text",        // image_size_source
	"jsonb",       // image_size_breakdown
	"integer",     // video_count
	"text",        // video_resolution
	"integer",     // video_duration_seconds
	"text",        // service_tier
	"text",        // reasoning_effort
	"text",        // inbound_endpoint
	"text",        // upstream_endpoint
	"boolean",     // cache_ttl_overridden
	"boolean",     // long_context_billing_applied
	"bigint",      // channel_id
	"text",        // model_mapping_chain
	"text",        // billing_tier
	"text",        // billing_mode
	"numeric",     // account_stats_cost
	"text",        // session_id
	"timestamptz", // created_at
}

const (
	usageLogCreateBatchMaxSize  = 64
	usageLogCreateBatchWindow   = 3 * time.Millisecond
	usageLogCreateBatchQueueCap = 4096
	usageLogCreateCancelWait    = 2 * time.Second

	usageLogBestEffortBatchMaxSize  = 256
	usageLogBestEffortBatchWindow   = 20 * time.Millisecond
	usageLogBestEffortBatchQueueCap = 32768
	usageLogBestEffortRecentTTL     = 30 * time.Second
)

type usageLogCreateRequest struct {
	log            *service.UsageLog
	prepared       usageLogInsertPrepared
	telemetryValue any
	shared         *usageLogCreateShared
	resultCh       chan usageLogCreateResult
}

type usageLogCreateResult struct {
	inserted bool
	err      error
}

type usageLogBestEffortRequest struct {
	prepared       usageLogInsertPrepared
	apiKeyID       int64
	telemetryValue any
	resultCh       chan usageLogCreateResult
}

type usageLogInsertPrepared struct {
	createdAt      time.Time
	requestID      string
	rateMultiplier float64
	requestType    int16
	args           []any
}

type usageLogBatchState struct {
	ID        int64
	CreatedAt time.Time
}

type usageLogBatchRow struct {
	RequestID string    `json:"request_id"`
	APIKeyID  int64     `json:"api_key_id"`
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Inserted  bool      `json:"inserted"`
}

type usageLogCreateShared struct {
	state atomic.Int32
}

const (
	usageLogCreateStateQueued int32 = iota
	usageLogCreateStateProcessing
	usageLogCreateStateCompleted
	usageLogCreateStateCanceled
)

func (r *usageLogRepository) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	if log == nil {
		return false, nil
	}

	if tx := dbent.TxFromContext(ctx); tx != nil {
		inserted, err := r.createSingle(ctx, tx.Client(), log)
		if err == nil && inserted {
			r.enqueueUsageAfterCommit(tx, log)
		}
		return inserted, err
	}
	var inserted bool
	var err error
	requestID := strings.TrimSpace(log.RequestID)
	if requestID == "" {
		inserted, err = r.createSingle(ctx, r.sql, log)
	} else {
		log.RequestID = requestID
		return r.createBatched(ctx, log)
	}
	if err == nil && inserted {
		r.enqueueUsage(log)
	}
	return inserted, err
}

func (r *usageLogRepository) CreateBestEffort(ctx context.Context, log *service.UsageLog) error {
	if log == nil {
		return nil
	}

	if tx := dbent.TxFromContext(ctx); tx != nil {
		inserted, err := r.createSingle(ctx, tx.Client(), log)
		if err == nil && inserted {
			r.enqueueUsageAfterCommit(tx, log)
		}
		return err
	}
	if r.db == nil {
		inserted, err := r.createSingle(ctx, r.sql, log)
		if err == nil && inserted {
			r.enqueueUsage(log)
		}
		return err
	}

	r.ensureBestEffortBatcher()
	if r.bestEffortBatchCh == nil {
		inserted, err := r.createSingle(ctx, r.sql, log)
		if err == nil && inserted {
			r.enqueueUsage(log)
		}
		return err
	}

	prepared := prepareUsageLogInsert(log)
	req := usageLogBestEffortRequest{
		prepared:       prepared,
		apiKeyID:       log.APIKeyID,
		telemetryValue: r.snapshotUsageTelemetry(log, prepared),
		resultCh:       make(chan usageLogCreateResult, 1),
	}
	if key, ok := r.bestEffortRecentKey(req.prepared.requestID, req.apiKeyID); ok {
		if _, exists := r.bestEffortRecent.Get(key); exists {
			return nil
		}
	}

	// 队列满时阻塞等待而非立即丢弃：批处理器持续排空队列，短暂等待即可入队。
	// 立即丢弃会造成“已扣费但无 usage_log”的永久数据缺口（issue #3656）；
	// 阻塞上限由调用方 ctx 期限约束，超时后由上层同步兜底。
	select {
	case r.bestEffortBatchCh <- req:
	case <-ctx.Done():
		return service.MarkUsageLogCreateDropped(ctx.Err())
	}

	select {
	case result := <-req.resultCh:
		return result.err
	case <-ctx.Done():
		return service.MarkUsageLogCreateDropped(ctx.Err())
	}
}

func (r *usageLogRepository) snapshotUsageTelemetry(
	log *service.UsageLog,
	prepared usageLogInsertPrepared,
) any {
	if r == nil || r.telemetry == nil || log == nil {
		return nil
	}
	snapshot := cloneUsageLog(log)
	snapshot.CreatedAt = prepared.createdAt
	snapshot.RequestID = prepared.requestID
	snapshot.RateMultiplier = prepared.rateMultiplier
	snapshot.RequestType = service.RequestType(prepared.requestType)
	if strings.TrimSpace(snapshot.RequestedModel) == "" {
		snapshot.RequestedModel = strings.TrimSpace(snapshot.Model)
	}
	return snapshot
}

func cloneUsageLog(log *service.UsageLog) *service.UsageLog {
	if log == nil {
		return nil
	}
	snapshot := *log
	snapshot.UpstreamModel = cloneUsageLogValue(log.UpstreamModel)
	snapshot.ChannelID = cloneUsageLogValue(log.ChannelID)
	snapshot.ModelMappingChain = cloneUsageLogValue(log.ModelMappingChain)
	snapshot.BillingTier = cloneUsageLogValue(log.BillingTier)
	snapshot.BillingMode = cloneUsageLogValue(log.BillingMode)
	snapshot.ServiceTier = cloneUsageLogValue(log.ServiceTier)
	snapshot.ReasoningEffort = cloneUsageLogValue(log.ReasoningEffort)
	snapshot.InboundEndpoint = cloneUsageLogValue(log.InboundEndpoint)
	snapshot.UpstreamEndpoint = cloneUsageLogValue(log.UpstreamEndpoint)
	snapshot.GroupID = cloneUsageLogValue(log.GroupID)
	snapshot.SubscriptionID = cloneUsageLogValue(log.SubscriptionID)
	snapshot.AccountRateMultiplier = cloneUsageLogValue(log.AccountRateMultiplier)
	snapshot.AccountStatsCost = cloneUsageLogValue(log.AccountStatsCost)
	snapshot.DurationMs = cloneUsageLogValue(log.DurationMs)
	snapshot.GatewayLatencyMs = cloneUsageLogValue(log.GatewayLatencyMs)
	snapshot.FirstTokenMs = cloneUsageLogValue(log.FirstTokenMs)
	snapshot.UserAgent = cloneUsageLogValue(log.UserAgent)
	snapshot.IPAddress = cloneUsageLogValue(log.IPAddress)
	snapshot.SessionID = cloneUsageLogValue(log.SessionID)
	snapshot.ImageSize = cloneUsageLogValue(log.ImageSize)
	snapshot.ImageInputSize = cloneUsageLogValue(log.ImageInputSize)
	snapshot.ImageOutputSize = cloneUsageLogValue(log.ImageOutputSize)
	snapshot.ImageSizeSource = cloneUsageLogValue(log.ImageSizeSource)
	snapshot.MediaType = cloneUsageLogValue(log.MediaType)
	snapshot.VideoResolution = cloneUsageLogValue(log.VideoResolution)
	snapshot.VideoDurationSeconds = cloneUsageLogValue(log.VideoDurationSeconds)
	if log.ImageSizeBreakdown != nil {
		snapshot.ImageSizeBreakdown = make(map[string]int, len(log.ImageSizeBreakdown))
		for size, count := range log.ImageSizeBreakdown {
			snapshot.ImageSizeBreakdown[size] = count
		}
	}

	// Relations are not usage_logs columns and can retain large or sensitive graphs.
	snapshot.User = nil
	snapshot.APIKey = nil
	snapshot.Account = nil
	snapshot.Group = nil
	snapshot.Subscription = nil
	return &snapshot
}

func cloneUsageLogValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func freezeUsageLogFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func (r *usageLogRepository) enqueueUsage(log *service.UsageLog) {
	r.enqueueUsageValue(log)
}

func (r *usageLogRepository) enqueueUsageValue(value any) {
	if r != nil && r.telemetry != nil && value != nil {
		r.telemetry.Enqueue(telemetryarchive.DatasetUsage, value)
	}
}

func (r *usageLogRepository) enqueueUsageAfterCommit(tx *dbent.Tx, log *service.UsageLog) {
	if r == nil || r.telemetry == nil || tx == nil || log == nil {
		return
	}
	value := r.snapshotUsageTelemetry(log, prepareUsageLogInsert(log))
	tx.OnCommit(func(next dbent.Committer) dbent.Committer {
		return dbent.CommitFunc(func(ctx context.Context, committedTx *dbent.Tx) error {
			if err := next.Commit(ctx, committedTx); err != nil {
				return err
			}
			r.enqueueUsageValue(value)
			return nil
		})
	})
}

func (r *usageLogRepository) createSingle(ctx context.Context, sqlq sqlExecutor, log *service.UsageLog) (bool, error) {
	prepared := prepareUsageLogInsert(log)
	return r.createSinglePrepared(ctx, sqlq, log, prepared)
}

func (r *usageLogRepository) createSinglePrepared(
	ctx context.Context,
	sqlq sqlExecutor,
	log *service.UsageLog,
	prepared usageLogInsertPrepared,
) (bool, error) {
	if sqlq == nil {
		sqlq = r.sql
	}
	if ctx != nil && ctx.Err() != nil {
		return false, service.MarkUsageLogCreateNotPersisted(ctx.Err())
	}

	query := `
		INSERT INTO usage_logs (
			user_id,
			api_key_id,
			account_id,
			request_id,
			model,
			requested_model,
			upstream_model,
			group_id,
			subscription_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			image_output_cost,
			image_input_tokens,
			image_input_cost,
			input_cost,
			output_cost,
			cache_creation_cost,
			cache_read_cost,
			total_cost,
			actual_cost,
			rate_multiplier,
			account_rate_multiplier,
			billing_type,
			request_type,
			stream,
			openai_ws_mode,
			duration_ms,
			gateway_latency_ms,
			first_token_ms,
			user_agent,
			ip_address,
			image_count,
			image_size,
			image_input_size,
			image_output_size,
			image_size_source,
			image_size_breakdown,
			video_count,
			video_resolution,
			video_duration_seconds,
			service_tier,
			reasoning_effort,
			inbound_endpoint,
			upstream_endpoint,
			cache_ttl_overridden,
			long_context_billing_applied,
			channel_id,
			model_mapping_chain,
			billing_tier,
			billing_mode,
			account_stats_cost,
			session_id,
			created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9,
			$10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23,
			$24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44, $45, $46, $47, $48, $49, $50, $51, $52, $53, $54, $55, $56, $57, $58
		)
		ON CONFLICT DO NOTHING
		RETURNING id, created_at
	`

	if err := scanSingleRow(ctx, sqlq, query, prepared.args, &log.ID, &log.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) && prepared.requestID != "" {
			selectQuery := "SELECT id, created_at FROM usage_logs WHERE request_id = $1 AND api_key_id = $2"
			if err := scanSingleRow(ctx, sqlq, selectQuery, []any{prepared.requestID, log.APIKeyID}, &log.ID, &log.CreatedAt); err != nil {
				return false, err
			}
			log.RateMultiplier = prepared.rateMultiplier
			return false, nil
		} else {
			return false, err
		}
	}
	log.RateMultiplier = prepared.rateMultiplier
	return true, nil
}

func (r *usageLogRepository) createBatched(ctx context.Context, log *service.UsageLog) (bool, error) {
	prepared := prepareUsageLogInsert(log)
	telemetryValue := r.snapshotUsageTelemetry(log, prepared)
	if r.db == nil {
		inserted, err := r.createSingle(ctx, r.sql, log)
		if err == nil && inserted {
			r.enqueueUsageValue(telemetryValue)
		}
		return inserted, err
	}
	r.ensureCreateBatcher()
	if r.createBatchCh == nil {
		inserted, err := r.createSingle(ctx, r.sql, log)
		if err == nil && inserted {
			r.enqueueUsageValue(telemetryValue)
		}
		return inserted, err
	}
	ownedLog := *log

	req := usageLogCreateRequest{
		log:            &ownedLog,
		prepared:       prepared,
		telemetryValue: telemetryValue,
		shared:         &usageLogCreateShared{},
		resultCh:       make(chan usageLogCreateResult, 1),
	}

	// 队列满时阻塞等待而非立即报错：本路径是 best-effort 丢弃后的最后兜底，
	// 立即失败会让日志永久丢失；阻塞上限由调用方 ctx 期限约束。
	select {
	case r.createBatchCh <- req:
	case <-ctx.Done():
		return false, service.MarkUsageLogCreateNotPersisted(ctx.Err())
	}

	select {
	case res := <-req.resultCh:
		copyUsageLogCreateResult(log, &ownedLog)
		return res.inserted, res.err
	case <-ctx.Done():
		if req.shared != nil && req.shared.state.CompareAndSwap(usageLogCreateStateQueued, usageLogCreateStateCanceled) {
			return false, service.MarkUsageLogCreateNotPersisted(ctx.Err())
		}
		timer := time.NewTimer(usageLogCreateCancelWait)
		defer timer.Stop()
		select {
		case res := <-req.resultCh:
			copyUsageLogCreateResult(log, &ownedLog)
			return res.inserted, res.err
		case <-timer.C:
			return false, ctx.Err()
		}
	}
}

func copyUsageLogCreateResult(dst, src *service.UsageLog) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.RateMultiplier = src.RateMultiplier
}

func (r *usageLogRepository) ensureCreateBatcher() {
	if r == nil || r.db == nil {
		return
	}
	// nil 检查必须在 Once 内部：在外层做无同步快路径读会与 Once 内的写构成数据竞争。
	r.createBatchOnce.Do(func() {
		if r.createBatchCh == nil {
			r.createBatchCh = make(chan usageLogCreateRequest, usageLogCreateBatchQueueCap)
			go r.runCreateBatcher(r.db)
		}
	})
}

func (r *usageLogRepository) ensureBestEffortBatcher() {
	if r == nil || r.db == nil {
		return
	}
	// 同 ensureCreateBatcher：nil 检查放在 Once 内部以避免数据竞争。
	r.bestEffortBatchOnce.Do(func() {
		if r.bestEffortBatchCh == nil {
			r.bestEffortBatchCh = make(chan usageLogBestEffortRequest, usageLogBestEffortBatchQueueCap)
			go r.runBestEffortBatcher(r.db)
		}
	})
}

func (r *usageLogRepository) runCreateBatcher(db *sql.DB) {
	for {
		first, ok := <-r.createBatchCh
		if !ok {
			return
		}

		batch := make([]usageLogCreateRequest, 0, usageLogCreateBatchMaxSize)
		batch = append(batch, first)

		timer := time.NewTimer(usageLogCreateBatchWindow)
	batchLoop:
		for len(batch) < usageLogCreateBatchMaxSize {
			select {
			case req, ok := <-r.createBatchCh:
				if !ok {
					break batchLoop
				}
				batch = append(batch, req)
			case <-timer.C:
				break batchLoop
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		r.flushCreateBatch(db, batch)
	}
}

func (r *usageLogRepository) runBestEffortBatcher(db *sql.DB) {
	for {
		first, ok := <-r.bestEffortBatchCh
		if !ok {
			return
		}

		batch := make([]usageLogBestEffortRequest, 0, usageLogBestEffortBatchMaxSize)
		batch = append(batch, first)

		timer := time.NewTimer(usageLogBestEffortBatchWindow)
	bestEffortLoop:
		for len(batch) < usageLogBestEffortBatchMaxSize {
			select {
			case req, ok := <-r.bestEffortBatchCh:
				if !ok {
					break bestEffortLoop
				}
				batch = append(batch, req)
			case <-timer.C:
				break bestEffortLoop
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		r.flushBestEffortBatch(db, batch)
	}
}

func (r *usageLogRepository) flushCreateBatch(db *sql.DB, batch []usageLogCreateRequest) {
	if len(batch) == 0 {
		return
	}

	uniqueOrder := make([]string, 0, len(batch))
	preparedByKey := make(map[string]usageLogInsertPrepared, len(batch))
	requestsByKey := make(map[string][]usageLogCreateRequest, len(batch))
	fallback := make([]usageLogCreateRequest, 0)

	for _, req := range batch {
		if req.log == nil {
			r.completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
			continue
		}
		if req.shared != nil && !req.shared.state.CompareAndSwap(usageLogCreateStateQueued, usageLogCreateStateProcessing) {
			if req.shared.state.Load() == usageLogCreateStateCanceled {
				r.completeUsageLogCreateRequest(req, usageLogCreateResult{
					inserted: false,
					err:      service.MarkUsageLogCreateNotPersisted(context.Canceled),
				})
				continue
			}
		}
		prepared := req.prepared
		if prepared.requestID == "" {
			fallback = append(fallback, req)
			continue
		}
		key := usageLogBatchKey(prepared.requestID, req.log.APIKeyID)
		if _, exists := requestsByKey[key]; !exists {
			uniqueOrder = append(uniqueOrder, key)
			preparedByKey[key] = prepared
		}
		requestsByKey[key] = append(requestsByKey[key], req)
	}

	if len(uniqueOrder) > 0 {
		insertedMap, stateMap, safeFallback, err := r.batchInsertUsageLogs(db, uniqueOrder, preparedByKey)
		if err != nil {
			if safeFallback {
				for _, key := range uniqueOrder {
					fallback = append(fallback, requestsByKey[key]...)
				}
			} else {
				for _, key := range uniqueOrder {
					reqs := requestsByKey[key]
					state, hasState := stateMap[key]
					inserted := insertedMap[key]
					for idx, req := range reqs {
						req.log.RateMultiplier = preparedByKey[key].rateMultiplier
						if hasState {
							req.log.ID = state.ID
							req.log.CreatedAt = state.CreatedAt
						}
						switch {
						case inserted && idx == 0:
							r.completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: true, err: nil})
						case inserted:
							r.completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
						case hasState:
							r.completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
						case idx == 0:
							r.completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: err})
						default:
							r.completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: false, err: nil})
						}
					}
				}
			}
		} else {
			for _, key := range uniqueOrder {
				reqs := requestsByKey[key]
				state, ok := stateMap[key]
				if !ok {
					for _, req := range reqs {
						r.completeUsageLogCreateRequest(req, usageLogCreateResult{
							inserted: false,
							err:      fmt.Errorf("usage log batch state missing for key=%s", key),
						})
					}
					continue
				}
				for idx, req := range reqs {
					req.log.ID = state.ID
					req.log.CreatedAt = state.CreatedAt
					req.log.RateMultiplier = preparedByKey[key].rateMultiplier
					r.completeUsageLogCreateRequest(req, usageLogCreateResult{
						inserted: idx == 0 && insertedMap[key],
						err:      nil,
					})
				}
			}
		}
	}

	if len(fallback) == 0 {
		return
	}

	for _, req := range fallback {
		fallbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		inserted, err := r.createSinglePrepared(fallbackCtx, db, req.log, req.prepared)
		cancel()
		r.completeUsageLogCreateRequest(req, usageLogCreateResult{inserted: inserted, err: err})
	}
}

func (r *usageLogRepository) flushBestEffortBatch(db *sql.DB, batch []usageLogBestEffortRequest) {
	if len(batch) == 0 {
		return
	}

	type bestEffortGroup struct {
		prepared usageLogInsertPrepared
		apiKeyID int64
		key      string
		reqs     []usageLogBestEffortRequest
	}

	groupsByKey := make(map[string]*bestEffortGroup, len(batch))
	groupOrder := make([]*bestEffortGroup, 0, len(batch))
	preparedList := make([]usageLogInsertPrepared, 0, len(batch))
	anonymousGroups := make([]*bestEffortGroup, 0, len(batch))

	for idx, req := range batch {
		prepared := req.prepared
		key := fmt.Sprintf("__best_effort_%d", idx)
		if prepared.requestID != "" {
			key = usageLogBatchKey(prepared.requestID, req.apiKeyID)
		}
		group, exists := groupsByKey[key]
		if !exists {
			group = &bestEffortGroup{
				prepared: prepared,
				apiKeyID: req.apiKeyID,
				key:      key,
			}
			groupsByKey[key] = group
			groupOrder = append(groupOrder, group)
			if prepared.requestID != "" {
				preparedList = append(preparedList, prepared)
			} else {
				anonymousGroups = append(anonymousGroups, group)
			}
		}
		group.reqs = append(group.reqs, req)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if len(anonymousGroups) > 0 {
		anonymousPrepared := make([]usageLogInsertPrepared, 0, len(anonymousGroups))
		for _, group := range anonymousGroups {
			anonymousPrepared = append(anonymousPrepared, group.prepared)
		}
		query, args := buildUsageLogBestEffortInsertQuery(anonymousPrepared)
		result, batchErr := db.ExecContext(ctx, query, args...)
		allInserted := false
		if batchErr == nil {
			rowsAffected, rowsErr := result.RowsAffected()
			batchErr = rowsErr
			allInserted = rowsErr == nil && rowsAffected == int64(len(anonymousGroups))
		}
		if batchErr != nil {
			logger.LegacyPrintf("repository.usage_log", "best-effort anonymous batch insert failed: %v", batchErr)
			for _, group := range anonymousGroups {
				inserted, singleErr := execUsageLogInsertNoResult(ctx, db, group.prepared)
				for idx, req := range group.reqs {
					r.completeUsageLogBestEffortRequest(req, usageLogCreateResult{
						inserted: inserted && idx == 0,
						err:      singleErr,
					})
				}
			}
		} else {
			for _, group := range anonymousGroups {
				for idx, req := range group.reqs {
					r.completeUsageLogBestEffortRequest(req, usageLogCreateResult{
						inserted: allInserted && idx == 0,
					})
				}
			}
		}
	}
	if len(preparedList) == 0 {
		return
	}

	query, args := buildUsageLogBestEffortInsertQuery(preparedList)
	query += "\nRETURNING request_id, api_key_id"
	insertedKeys, err := queryUsageLogBestEffortInsertedKeys(ctx, db, query, args)
	if err != nil {
		logger.LegacyPrintf("repository.usage_log", "best-effort batch insert failed: %v", err)
		for _, group := range groupOrder {
			if group.prepared.requestID == "" {
				continue
			}
			inserted, singleErr := execUsageLogInsertNoResult(ctx, db, group.prepared)
			if singleErr != nil {
				logger.LegacyPrintf("repository.usage_log", "best-effort single fallback insert failed: %v", singleErr)
			} else if group.prepared.requestID != "" && r != nil && r.bestEffortRecent != nil {
				r.bestEffortRecent.SetDefault(group.key, struct{}{})
			}
			for idx, req := range group.reqs {
				r.completeUsageLogBestEffortRequest(req, usageLogCreateResult{
					inserted: inserted && idx == 0,
					err:      singleErr,
				})
			}
		}
		return
	}
	for _, group := range groupOrder {
		if group.prepared.requestID == "" {
			continue
		}
		if group.prepared.requestID != "" && r != nil && r.bestEffortRecent != nil {
			r.bestEffortRecent.SetDefault(group.key, struct{}{})
		}
		for idx, req := range group.reqs {
			r.completeUsageLogBestEffortRequest(req, usageLogCreateResult{
				inserted: insertedKeys[group.key] && idx == 0,
			})
		}
	}
}

func (r *usageLogRepository) completeUsageLogBestEffortRequest(
	req usageLogBestEffortRequest,
	result usageLogCreateResult,
) {
	if result.err == nil && result.inserted {
		r.enqueueUsageValue(req.telemetryValue)
	}
	sendUsageLogBestEffortResult(req.resultCh, result)
}

func queryUsageLogBestEffortInsertedKeys(
	ctx context.Context,
	db *sql.DB,
	query string,
	args []any,
) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	inserted := make(map[string]bool)
	for rows.Next() {
		var requestID string
		var apiKeyID int64
		if err := rows.Scan(&requestID, &apiKeyID); err != nil {
			return nil, err
		}
		inserted[usageLogBatchKey(requestID, apiKeyID)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func sendUsageLogBestEffortResult(ch chan usageLogCreateResult, result usageLogCreateResult) {
	if ch == nil {
		return
	}
	select {
	case ch <- result:
	default:
	}
}

func (r *usageLogRepository) completeUsageLogCreateRequest(req usageLogCreateRequest, res usageLogCreateResult) {
	if res.err == nil && res.inserted {
		r.enqueueUsageValue(req.telemetryValue)
	}
	if req.shared != nil {
		req.shared.state.Store(usageLogCreateStateCompleted)
	}
	sendUsageLogCreateResult(req.resultCh, res)
}

func (r *usageLogRepository) batchInsertUsageLogs(db *sql.DB, keys []string, preparedByKey map[string]usageLogInsertPrepared) (map[string]bool, map[string]usageLogBatchState, bool, error) {
	if len(keys) == 0 {
		return map[string]bool{}, map[string]usageLogBatchState{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query, args := buildUsageLogBatchInsertQuery(keys, preparedByKey)
	var payload []byte
	if err := db.QueryRowContext(ctx, query, args...).Scan(&payload); err != nil {
		return nil, nil, true, err
	}
	var rows []usageLogBatchRow
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, nil, false, err
	}
	insertedMap := make(map[string]bool, len(keys))
	stateMap := make(map[string]usageLogBatchState, len(keys))
	for _, row := range rows {
		key := usageLogBatchKey(row.RequestID, row.APIKeyID)
		insertedMap[key] = row.Inserted
		stateMap[key] = usageLogBatchState{
			ID:        row.ID,
			CreatedAt: row.CreatedAt,
		}
	}
	if len(stateMap) != len(keys) {
		return insertedMap, stateMap, false, fmt.Errorf("usage log batch state count mismatch: got=%d want=%d", len(stateMap), len(keys))
	}
	return insertedMap, stateMap, false, nil
}

func buildUsageLogBatchInsertQuery(keys []string, preparedByKey map[string]usageLogInsertPrepared) (string, []any) {
	var query strings.Builder
	_, _ = query.WriteString(`
		WITH input (
			input_idx,
			user_id,
			api_key_id,
			account_id,
			request_id,
			model,
			requested_model,
			upstream_model,
			group_id,
			subscription_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			image_output_cost,
			image_input_tokens,
			image_input_cost,
			input_cost,
			output_cost,
			cache_creation_cost,
			cache_read_cost,
			total_cost,
			actual_cost,
			rate_multiplier,
			account_rate_multiplier,
			billing_type,
			request_type,
			stream,
			openai_ws_mode,
			duration_ms,
			gateway_latency_ms,
			first_token_ms,
			user_agent,
			ip_address,
			image_count,
			image_size,
			image_input_size,
			image_output_size,
			image_size_source,
			image_size_breakdown,
			video_count,
			video_resolution,
			video_duration_seconds,
			service_tier,
			reasoning_effort,
			inbound_endpoint,
			upstream_endpoint,
			cache_ttl_overridden,
			long_context_billing_applied,
			channel_id,
			model_mapping_chain,
			billing_tier,
			billing_mode,
			account_stats_cost,
			session_id,
			created_at
		) AS (VALUES `)

	// Each batch row prepends the synthetic input_index before the 58
	// usage-log column values.
	args := make([]any, 0, len(keys)*58)
	argPos := 1
	for idx, key := range keys {
		if idx > 0 {
			_, _ = query.WriteString(",")
		}
		_, _ = query.WriteString("(")
		_, _ = query.WriteString("$")
		_, _ = query.WriteString(strconv.Itoa(argPos))
		args = append(args, idx)
		argPos++
		prepared := preparedByKey[key]
		for i := 0; i < len(prepared.args); i++ {
			_, _ = query.WriteString(",")
			_, _ = query.WriteString("$")
			_, _ = query.WriteString(strconv.Itoa(argPos))
			if i < len(usageLogInsertArgTypes) {
				_, _ = query.WriteString("::")
				_, _ = query.WriteString(usageLogInsertArgTypes[i])
			}
			argPos++
		}
		_, _ = query.WriteString(")")
		args = append(args, prepared.args...)
	}
	_, _ = query.WriteString(`
		),
		inserted AS (
			INSERT INTO usage_logs (
				user_id,
				api_key_id,
				account_id,
				request_id,
				model,
				requested_model,
				upstream_model,
				group_id,
				subscription_id,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				cache_creation_5m_tokens,
				cache_creation_1h_tokens,
				image_output_tokens,
				image_output_cost,
				image_input_tokens,
				image_input_cost,
				input_cost,
				output_cost,
				cache_creation_cost,
				cache_read_cost,
				total_cost,
				actual_cost,
				rate_multiplier,
				account_rate_multiplier,
				billing_type,
				request_type,
				stream,
				openai_ws_mode,
				duration_ms,
				gateway_latency_ms,
				first_token_ms,
				user_agent,
				ip_address,
				image_count,
				image_size,
				image_input_size,
				image_output_size,
				image_size_source,
				image_size_breakdown,
				video_count,
				video_resolution,
				video_duration_seconds,
				service_tier,
				reasoning_effort,
				inbound_endpoint,
				upstream_endpoint,
				cache_ttl_overridden,
				long_context_billing_applied,
				channel_id,
				model_mapping_chain,
				billing_tier,
				billing_mode,
				account_stats_cost,
				session_id,
				created_at
			)
			SELECT
				user_id,
				api_key_id,
				account_id,
				request_id,
				model,
				requested_model,
				upstream_model,
				group_id,
				subscription_id,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				cache_creation_5m_tokens,
				cache_creation_1h_tokens,
				image_output_tokens,
				image_output_cost,
				image_input_tokens,
				image_input_cost,
				input_cost,
				output_cost,
				cache_creation_cost,
				cache_read_cost,
				total_cost,
				actual_cost,
				rate_multiplier,
				account_rate_multiplier,
				billing_type,
				request_type,
				stream,
				openai_ws_mode,
				duration_ms,
				gateway_latency_ms,
				first_token_ms,
				user_agent,
				ip_address,
				image_count,
				image_size,
				image_input_size,
				image_output_size,
				image_size_source,
				image_size_breakdown,
				video_count,
				video_resolution,
				video_duration_seconds,
				service_tier,
				reasoning_effort,
				inbound_endpoint,
				upstream_endpoint,
				cache_ttl_overridden,
				long_context_billing_applied,
				channel_id,
				model_mapping_chain,
				billing_tier,
				billing_mode,
				account_stats_cost,
				session_id,
				created_at
			FROM input
			ON CONFLICT DO NOTHING
			RETURNING request_id, api_key_id, id, created_at
		),
		resolved AS (
			SELECT
				input.input_idx,
				input.request_id,
				input.api_key_id,
				COALESCE(inserted.id, existing.id) AS id,
				COALESCE(inserted.created_at, existing.created_at) AS created_at,
				(inserted.id IS NOT NULL) AS inserted
			FROM input
			LEFT JOIN inserted
				ON inserted.request_id = input.request_id
				AND inserted.api_key_id = input.api_key_id
			LEFT JOIN usage_logs existing
				ON existing.request_id = input.request_id
				AND existing.api_key_id = input.api_key_id
		)
		SELECT COALESCE(
			json_agg(
				json_build_object(
					'request_id', resolved.request_id,
					'api_key_id', resolved.api_key_id,
					'id', resolved.id,
					'created_at', resolved.created_at,
					'inserted', resolved.inserted
				)
				ORDER BY resolved.input_idx
			),
			'[]'::json
		)
		FROM resolved
	`)
	return query.String(), args
}

func buildUsageLogBestEffortInsertQuery(preparedList []usageLogInsertPrepared) (string, []any) {
	var query strings.Builder
	_, _ = query.WriteString(`
		WITH input (
			user_id,
			api_key_id,
			account_id,
			request_id,
			model,
			requested_model,
			upstream_model,
			group_id,
			subscription_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			image_output_cost,
			image_input_tokens,
			image_input_cost,
			input_cost,
			output_cost,
			cache_creation_cost,
			cache_read_cost,
			total_cost,
			actual_cost,
			rate_multiplier,
			account_rate_multiplier,
			billing_type,
			request_type,
			stream,
			openai_ws_mode,
			duration_ms,
			gateway_latency_ms,
			first_token_ms,
			user_agent,
			ip_address,
			image_count,
			image_size,
			image_input_size,
			image_output_size,
			image_size_source,
			image_size_breakdown,
			video_count,
			video_resolution,
			video_duration_seconds,
			service_tier,
			reasoning_effort,
			inbound_endpoint,
			upstream_endpoint,
			cache_ttl_overridden,
			long_context_billing_applied,
			channel_id,
			model_mapping_chain,
			billing_tier,
			billing_mode,
			account_stats_cost,
			session_id,
			created_at
		) AS (VALUES `)

	args := make([]any, 0, len(preparedList)*58)
	argPos := 1
	for idx, prepared := range preparedList {
		if idx > 0 {
			_, _ = query.WriteString(",")
		}
		_, _ = query.WriteString("(")
		for i := 0; i < len(prepared.args); i++ {
			if i > 0 {
				_, _ = query.WriteString(",")
			}
			_, _ = query.WriteString("$")
			_, _ = query.WriteString(strconv.Itoa(argPos))
			if i < len(usageLogInsertArgTypes) {
				_, _ = query.WriteString("::")
				_, _ = query.WriteString(usageLogInsertArgTypes[i])
			}
			argPos++
		}
		_, _ = query.WriteString(")")
		args = append(args, prepared.args...)
	}

	_, _ = query.WriteString(`
		)
		INSERT INTO usage_logs (
			user_id,
			api_key_id,
			account_id,
			request_id,
			model,
			requested_model,
			upstream_model,
			group_id,
			subscription_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			image_output_cost,
			image_input_tokens,
			image_input_cost,
			input_cost,
			output_cost,
			cache_creation_cost,
			cache_read_cost,
			total_cost,
			actual_cost,
			rate_multiplier,
			account_rate_multiplier,
			billing_type,
			request_type,
			stream,
			openai_ws_mode,
			duration_ms,
			gateway_latency_ms,
			first_token_ms,
			user_agent,
			ip_address,
			image_count,
			image_size,
			image_input_size,
			image_output_size,
			image_size_source,
			image_size_breakdown,
			video_count,
			video_resolution,
			video_duration_seconds,
			service_tier,
			reasoning_effort,
			inbound_endpoint,
			upstream_endpoint,
			cache_ttl_overridden,
			long_context_billing_applied,
			channel_id,
			model_mapping_chain,
			billing_tier,
			billing_mode,
			account_stats_cost,
			session_id,
			created_at
		)
		SELECT
			user_id,
			api_key_id,
			account_id,
			request_id,
			model,
			requested_model,
			upstream_model,
			group_id,
			subscription_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			image_output_cost,
			image_input_tokens,
			image_input_cost,
			input_cost,
			output_cost,
			cache_creation_cost,
			cache_read_cost,
			total_cost,
			actual_cost,
			rate_multiplier,
			account_rate_multiplier,
			billing_type,
			request_type,
			stream,
			openai_ws_mode,
			duration_ms,
			gateway_latency_ms,
			first_token_ms,
			user_agent,
			ip_address,
			image_count,
			image_size,
			image_input_size,
			image_output_size,
			image_size_source,
			image_size_breakdown,
			video_count,
			video_resolution,
			video_duration_seconds,
			service_tier,
			reasoning_effort,
			inbound_endpoint,
			upstream_endpoint,
			cache_ttl_overridden,
			long_context_billing_applied,
			channel_id,
			model_mapping_chain,
			billing_tier,
			billing_mode,
			account_stats_cost,
			session_id,
			created_at
			FROM input
			ON CONFLICT DO NOTHING
		`)

	return query.String(), args
}

func execUsageLogInsertNoResult(ctx context.Context, sqlq sqlExecutor, prepared usageLogInsertPrepared) (bool, error) {
	result, err := sqlq.ExecContext(ctx, `
		INSERT INTO usage_logs (
			user_id,
			api_key_id,
			account_id,
			request_id,
			model,
			requested_model,
			upstream_model,
			group_id,
			subscription_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			cache_creation_5m_tokens,
			cache_creation_1h_tokens,
			image_output_tokens,
			image_output_cost,
			image_input_tokens,
			image_input_cost,
			input_cost,
			output_cost,
			cache_creation_cost,
			cache_read_cost,
			total_cost,
			actual_cost,
			rate_multiplier,
			account_rate_multiplier,
			billing_type,
			request_type,
			stream,
			openai_ws_mode,
			duration_ms,
			gateway_latency_ms,
			first_token_ms,
			user_agent,
			ip_address,
			image_count,
			image_size,
			image_input_size,
			image_output_size,
			image_size_source,
			image_size_breakdown,
			video_count,
			video_resolution,
			video_duration_seconds,
			service_tier,
			reasoning_effort,
			inbound_endpoint,
			upstream_endpoint,
			cache_ttl_overridden,
			long_context_billing_applied,
			channel_id,
			model_mapping_chain,
			billing_tier,
			billing_mode,
			account_stats_cost,
			session_id,
			created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9,
			$10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23,
			$24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44, $45, $46, $47, $48, $49, $50, $51, $52, $53, $54, $55, $56, $57, $58
		)
		ON CONFLICT DO NOTHING
		`, prepared.args...)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

func prepareUsageLogInsert(log *service.UsageLog) usageLogInsertPrepared {
	createdAt := log.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	requestID := strings.TrimSpace(log.RequestID)
	log.RequestID = requestID

	rateMultiplier := log.RateMultiplier
	log.SyncRequestTypeAndLegacyFields()
	requestType := int16(log.RequestType)

	groupID := nullInt64(log.GroupID)
	subscriptionID := nullInt64(log.SubscriptionID)
	duration := nullInt(log.DurationMs)
	gatewayLatency := nullInt(log.GatewayLatencyMs)
	firstToken := nullInt(log.FirstTokenMs)
	userAgent := nullString(log.UserAgent)
	ipAddress := nullString(log.IPAddress)
	imageSize := nullString(log.ImageSize)
	imageInputSize := nullString(log.ImageInputSize)
	imageOutputSize := nullString(log.ImageOutputSize)
	imageSizeSource := nullString(log.ImageSizeSource)
	imageSizeBreakdown := nullStringIntMapJSON(log.ImageSizeBreakdown)
	videoResolution := nullString(log.VideoResolution)
	videoDurationSeconds := nullInt64(log.VideoDurationSeconds)
	serviceTier := nullString(log.ServiceTier)
	reasoningEffort := nullString(log.ReasoningEffort)
	inboundEndpoint := nullString(log.InboundEndpoint)
	upstreamEndpoint := nullString(log.UpstreamEndpoint)
	channelID := nullInt64(log.ChannelID)
	modelMappingChain := nullString(log.ModelMappingChain)
	billingTier := nullString(log.BillingTier)
	billingMode := nullString(log.BillingMode)
	sessionID := nullString(log.SessionID)
	requestedModel := strings.TrimSpace(log.RequestedModel)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(log.Model)
	}
	upstreamModel := nullString(log.UpstreamModel)

	var requestIDArg any
	if requestID != "" {
		requestIDArg = requestID
	}

	return usageLogInsertPrepared{
		createdAt:      createdAt,
		requestID:      requestID,
		rateMultiplier: rateMultiplier,
		requestType:    requestType,
		args: []any{
			log.UserID,
			log.APIKeyID,
			log.AccountID,
			requestIDArg,
			log.Model,
			nullString(&requestedModel),
			upstreamModel,
			groupID,
			subscriptionID,
			log.InputTokens,
			log.OutputTokens,
			log.CacheCreationTokens,
			log.CacheReadTokens,
			log.CacheCreation5mTokens,
			log.CacheCreation1hTokens,
			log.ImageOutputTokens,
			log.ImageOutputCost,
			log.ImageInputTokens,
			log.ImageInputCost,
			log.InputCost,
			log.OutputCost,
			log.CacheCreationCost,
			log.CacheReadCost,
			log.TotalCost,
			log.ActualCost,
			rateMultiplier,
			freezeUsageLogFloat64(log.AccountRateMultiplier),
			log.BillingType,
			requestType,
			log.Stream,
			log.OpenAIWSMode,
			duration,
			gatewayLatency,
			firstToken,
			userAgent,
			ipAddress,
			log.ImageCount,
			imageSize,
			imageInputSize,
			imageOutputSize,
			imageSizeSource,
			imageSizeBreakdown,
			log.VideoCount,
			videoResolution,
			videoDurationSeconds,
			serviceTier,
			reasoningEffort,
			inboundEndpoint,
			upstreamEndpoint,
			log.CacheTTLOverridden,
			log.LongContextBillingApplied,
			channelID,
			modelMappingChain,
			billingTier,
			billingMode,
			freezeUsageLogFloat64(log.AccountStatsCost), // account_stats_cost
			sessionID, // session_id
			createdAt,
		},
	}
}

func usageLogBatchKey(requestID string, apiKeyID int64) string {
	return requestID + "\x1f" + strconv.FormatInt(apiKeyID, 10)
}

func sendUsageLogCreateResult(ch chan usageLogCreateResult, res usageLogCreateResult) {
	if ch == nil {
		return
	}
	select {
	case ch <- res:
	default:
	}
}

func (r *usageLogRepository) bestEffortRecentKey(requestID string, apiKeyID int64) (string, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || r == nil || r.bestEffortRecent == nil {
		return "", false
	}
	return usageLogBatchKey(requestID, apiKeyID), true
}
