package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/lib/pq"
)

// userSpendAgg accumulates a single user's window aggregate while merging the
// rollup and raw-remainder reads.
type userSpendAgg struct {
	cost     float64
	requests int64
	tokens   int64
}

// TK: rollup-backed read paths for the two heaviest admin-page aggregations.
//
// Both previously scanned the full 30-day created_at window of usage_logs
// (~1.25M rows / 845K buffers / 2.7s on prod). A wide-window aggregation over
// ~half a 2-month table cannot be sped up by ANY index — the planner correctly
// prefers a bitmap heap scan, and forcing a covering/partial index is the same
// cost or worse (verified by EXPLAIN ANALYZE at prod scale). The structural fix
// is to read pre-aggregated per-(user, platform, day) rows from
// usage_dashboard_user_platform_daily (~hundreds of rows, sub-millisecond)
// instead of the raw logs.
//
// Exact-semantics decomposition (works for ANY [start,end) window and timezone):
//
//	[start, end) = rawHead ∪ rollupMiddle ∪ rawTail
//
//	  rollupMiddle = [ceilDay(start), floorDay(min(end, today)))  -- FULL completed
//	                 server-TZ days strictly inside the window, served by the rollup
//	  rawHead      = [start, ceilDay(start))                      -- partial leading day
//	  rawTail      = [max(rollupEnd, start), end)                 -- partial trailing day
//	                 plus all of today (today's bucket is still mutating)
//
// The rollup only ever covers complete, immutable past days, so it reproduces
// the raw sum exactly; every partial/today slice is read live from usage_logs (a
// narrow created_at window the existing index serves cheaply). This keeps "today"
// numbers exact and bounds rollup staleness to one aggregation interval (~1 min),
// well inside the page's existing 30s cache.

// usageRollupWindow describes how a [start,end) window splits into a completed-day
// rollup span and the raw remainder(s).
type usageRollupWindow struct {
	rollupStartDay time.Time // inclusive, server-TZ midnight; zero if no rollup span
	rollupEndDay   time.Time // exclusive, server-TZ midnight; zero if no rollup span
	hasRollup      bool
	rawSpans       [][2]time.Time // half-open [from,to) raw remainders to read from usage_logs
}

// planUsageRollupWindow computes the decomposition above. `now` and the
// server-TZ day boundaries are taken from the timezone package so the day grain
// matches usage_dashboard_user_platform_daily.bucket_date exactly.
func planUsageRollupWindow(start, end, rollupFloorDay time.Time, hasRollupData bool) usageRollupWindow {
	today := timezone.Today()

	// Coverage floor. The rollup only holds data from rollupFloorDay onward.
	// Critically, right after the tk_034 migration the table is empty / recent-only
	// while the SHARED dashboard-aggregation watermark has already advanced past
	// historical days (it has driven the system-wide rollups for months), so the
	// incremental feeder never goes back to backfill those days for the new table.
	// If the read path trusted the rollup for every completed day in the window,
	// the admin Users page + spending ranking would silently UNDERCOUNT history for
	// up to the full window length after deploy, until the rolling window refills.
	// So: completed days BEFORE the floor (or the entire window while the table is
	// still empty) are read from raw usage_logs instead — exact, just slower, and
	// it self-heals as the feeder rolls forward. This also keeps reads correct if
	// the aggregation service is ever down or lagging.
	if !hasRollupData {
		w := usageRollupWindow{}
		if end.After(start) {
			w.rawSpans = append(w.rawSpans, [2]time.Time{start, end})
		}
		return w
	}

	// Last instant the rollup may cover: completed days only, never today, never
	// past the window end.
	cap := today
	if end.Before(cap) {
		cap = end
	}

	rollupStart := ceilDayServerTZ(start)
	// Clamp the rollup span up to the floor; days below it fall into the raw head.
	if rollupFloorDay.After(rollupStart) {
		rollupStart = rollupFloorDay
	}
	rollupEnd := timezone.StartOfDay(cap) // floor to server-TZ midnight

	w := usageRollupWindow{}
	if rollupEnd.After(rollupStart) {
		w.hasRollup = true
		w.rollupStartDay = rollupStart
		w.rollupEndDay = rollupEnd
		// Raw head: everything before the rollup span start. Normally just the
		// partial leading day, but when rollupStart was clamped UP to the coverage
		// floor (cold start), this span covers ALL the completed days below the
		// floor too — do not assume it is sub-24h.
		if rollupStart.After(start) {
			w.rawSpans = append(w.rawSpans, [2]time.Time{start, rollupStart})
		}
		// Raw tail: from the end of the rollup span to the window end (covers the
		// partial trailing day and all of today).
		if end.After(rollupEnd) {
			w.rawSpans = append(w.rawSpans, [2]time.Time{rollupEnd, end})
		}
	} else {
		// No complete day fits — serve the whole window from raw logs.
		if end.After(start) {
			w.rawSpans = append(w.rawSpans, [2]time.Time{start, end})
		}
	}
	return w
}

// ceilDayServerTZ returns the start of the first full server-TZ day at or after t.
func ceilDayServerTZ(t time.Time) time.Time {
	floor := timezone.StartOfDay(t)
	if floor.Equal(t.In(floor.Location())) {
		return floor
	}
	return floor.Add(24 * time.Hour)
}

// userPlatformRollupFloorDay returns the earliest server-TZ day the rollup table
// has any data for, as a server-TZ midnight. Returns hasData=false when the table
// is empty. Callers pass this into planUsageRollupWindow so completed days the
// rollup does not yet cover are read from raw usage_logs (see the cold-start note
// there). The date is fetched as text and parsed in the server location to avoid
// any timestamp/timezone drift between the DATE column and the day-boundary math.
func (r *usageLogRepository) userPlatformRollupFloorDay(ctx context.Context) (time.Time, bool, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT to_char(MIN(bucket_date), 'YYYY-MM-DD') FROM usage_dashboard_user_platform_daily`)
	if err != nil {
		return time.Time{}, false, err
	}
	var s sql.NullString
	for rows.Next() {
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return time.Time{}, false, err
		}
	}
	if err := rows.Close(); err != nil {
		return time.Time{}, false, err
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, err
	}
	if !s.Valid || s.String == "" {
		return time.Time{}, false, nil
	}
	loc := timezone.Today().Location()
	day, err := time.ParseInLocation("2006-01-02", s.String, loc)
	if err != nil {
		return time.Time{}, false, err
	}
	return day, true, nil
}

// getBatchUserUsageStatsRollup answers GetBatchUserUsageStats from the rollup for
// completed days plus raw usage_logs for the partial/today slices. Output is
// byte-identical to the legacy single-query path: per-(user, effective-platform)
// total_cost over [startTime,endTime) and today_cost over [today, ...). Only
// billed rows (actual_cost > 0) contribute, matching usageLogSuccessFilterUL.
func (r *usageLogRepository) getBatchUserUsageStatsRollup(ctx context.Context, userIDs []int64, startTime, endTime time.Time) (map[int64]*BatchUserUsageStats, error) {
	result := make(map[int64]*BatchUserUsageStats, len(userIDs))
	for _, id := range userIDs {
		result[id] = &BatchUserUsageStats{UserID: id}
	}

	today := timezone.Today()
	// Two accumulators mirror the legacy single-query semantics exactly: the
	// per-user total includes EVERY row (even one whose effective platform is
	// empty), while the per-(user, platform) breakdown excludes empty-platform
	// rows. (Empty platform is near-impossible — accounts.platform is NOT NULL —
	// but the legacy query still folded such rows into the user total, so we do
	// too.)
	type acc struct {
		total float64
		today float64
	}
	type key struct {
		user     int64
		platform string
	}
	userTotal := make(map[int64]*acc)
	byPlat := make(map[key]*acc)
	add := func(user int64, platform string, total, todayCost float64) {
		ut, ok := userTotal[user]
		if !ok {
			ut = &acc{}
			userTotal[user] = ut
		}
		ut.total += total
		ut.today += todayCost
		if platform == "" {
			return
		}
		k := key{user: user, platform: platform}
		a, ok := byPlat[k]
		if !ok {
			a = &acc{}
			byPlat[k] = a
		}
		a.total += total
		a.today += todayCost
	}

	floorDay, hasRollupData, err := r.userPlatformRollupFloorDay(ctx)
	if err != nil {
		return nil, err
	}
	win := planUsageRollupWindow(startTime, endTime, floorDay, hasRollupData)

	// Rollup portion: completed days only. By construction these days are < today,
	// so they never carry today_cost; only billed rows are summed (actual_cost is
	// the rollup's own sum, but a per-day row may include zero-cost rows in its
	// request count — for cost the sum already excludes nothing, and zero-cost
	// adds 0, so the billed-only total equals the rollup actual_cost sum).
	if win.hasRollup {
		const q = `
			SELECT user_id, platform, COALESCE(SUM(actual_cost), 0)
			FROM usage_dashboard_user_platform_daily
			WHERE user_id = ANY($1)
			  AND bucket_date >= $2::date AND bucket_date < $3::date
			GROUP BY user_id, platform
		`
		rows, err := r.sql.QueryContext(ctx, q, pq.Array(userIDs), win.rollupStartDay, win.rollupEndDay)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid int64
			var platform string
			var total float64
			if err := rows.Scan(&uid, &platform, &total); err != nil {
				_ = rows.Close()
				return nil, err
			}
			add(uid, platform, total, 0)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Raw portion: each partial/today slice. today_cost is the part of the slice
	// that is >= today.
	for _, span := range win.rawSpans {
		from, to := span[0], span[1]
		const q = `
			SELECT
				ul.user_id,
				` + usageLogEffectivePlatformExpr + ` AS platform,
				COALESCE(SUM(ul.actual_cost), 0) AS total_cost,
				COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $4), 0) AS today_cost
			FROM usage_logs ul
			LEFT JOIN groups g ON g.id = ul.group_id
			LEFT JOIN accounts a ON a.id = ul.account_id
			WHERE ul.user_id = ANY($1)
			  AND ul.created_at >= $2 AND ul.created_at < $3
			  AND ` + usageLogSuccessFilterUL + `
			GROUP BY ul.user_id, ` + usageLogEffectivePlatformExpr + `
		`
		rows, err := r.sql.QueryContext(ctx, q, pq.Array(userIDs), from, to, today)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid int64
			var platform sql.NullString
			var total, todayCost float64
			if err := rows.Scan(&uid, &platform, &total, &todayCost); err != nil {
				_ = rows.Close()
				return nil, err
			}
			// platform.String is "" when NULL, which add() folds into the user
			// total without emitting a ByPlatform entry — matching the legacy query
			// that summed every row into the total regardless of platform.
			add(uid, platform.String, total, todayCost)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	for uid, a := range userTotal {
		if stats, ok := result[uid]; ok {
			stats.TotalActualCost += a.total
			stats.TodayActualCost += a.today
		}
	}
	for k, a := range byPlat {
		if stats, ok := result[k.user]; ok {
			stats.ByPlatform = append(stats.ByPlatform, usagestats.PlatformUsage{
				Platform:        k.platform,
				TotalActualCost: a.total,
				TodayActualCost: a.today,
			})
		}
	}
	return result, nil
}

// getUserSpendingRankingRollup answers GetUserSpendingRanking from the rollup for
// completed days plus raw usage_logs for the partial/today slices. Unlike the
// batch path, this counts requests and sums tokens over EVERY row (including
// actual_cost = 0): the rollup's total_requests / token columns already include
// zero-cost rows, and the raw slices use no actual_cost filter, so the merged
// totals match the legacy CTE exactly. email + ordering + window totals are
// derived in Go to preserve the legacy output shape.
func (r *usageLogRepository) getUserSpendingRankingRollup(ctx context.Context, startTime, endTime time.Time, limit int) (*UserSpendingRankingResponse, error) {
	byUser := make(map[int64]*userSpendAgg)
	addRow := func(user int64, cost float64, reqs, toks int64) {
		a, ok := byUser[user]
		if !ok {
			a = &userSpendAgg{}
			byUser[user] = a
		}
		a.cost += cost
		a.requests += reqs
		a.tokens += toks
	}

	floorDay, hasRollupData, err := r.userPlatformRollupFloorDay(ctx)
	if err != nil {
		return nil, err
	}
	win := planUsageRollupWindow(startTime, endTime, floorDay, hasRollupData)

	if win.hasRollup {
		const q = `
			SELECT
				user_id,
				COALESCE(SUM(actual_cost), 0),
				COALESCE(SUM(total_requests), 0),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)
			FROM usage_dashboard_user_platform_daily
			WHERE bucket_date >= $1::date AND bucket_date < $2::date
			GROUP BY user_id
		`
		rows, err := r.sql.QueryContext(ctx, q, win.rollupStartDay, win.rollupEndDay)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid, reqs, toks int64
			var cost float64
			if err := rows.Scan(&uid, &cost, &reqs, &toks); err != nil {
				_ = rows.Close()
				return nil, err
			}
			addRow(uid, cost, reqs, toks)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	for _, span := range win.rawSpans {
		from, to := span[0], span[1]
		const q = `
			SELECT
				user_id,
				COALESCE(SUM(actual_cost), 0),
				COUNT(*),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY user_id
		`
		rows, err := r.sql.QueryContext(ctx, q, from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid, reqs, toks int64
			var cost float64
			if err := rows.Scan(&uid, &cost, &reqs, &toks); err != nil {
				_ = rows.Close()
				return nil, err
			}
			addRow(uid, cost, reqs, toks)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return r.assembleUserSpendingRanking(ctx, byUser, limit)
}

// assembleUserSpendingRanking turns the per-user aggregate into the legacy
// response: window totals over ALL users, top-N ordered by (actual_cost DESC,
// tokens DESC, user_id ASC), each item enriched with the user email.
func (r *usageLogRepository) assembleUserSpendingRanking(ctx context.Context, byUser map[int64]*userSpendAgg, limit int) (*UserSpendingRankingResponse, error) {
	// Window-wide totals across every user (the legacy SUM() OVER ()).
	resp := &UserSpendingRankingResponse{Ranking: make([]UserSpendingRankingItem, 0, len(byUser))}
	userIDs := make([]int64, 0, len(byUser))
	for uid, a := range byUser {
		resp.TotalActualCost += a.cost
		resp.TotalRequests += a.requests
		resp.TotalTokens += a.tokens
		userIDs = append(userIDs, uid)
	}

	emails, err := r.fetchUserEmails(ctx, userIDs)
	if err != nil {
		return nil, err
	}

	items := make([]UserSpendingRankingItem, 0, len(byUser))
	for uid, a := range byUser {
		items = append(items, UserSpendingRankingItem{
			UserID:     uid,
			Email:      emails[uid],
			ActualCost: a.cost,
			Requests:   a.requests,
			Tokens:     a.tokens,
		})
	}
	sortUserSpendingRankingItems(items)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	resp.Ranking = items
	return resp, nil
}

// fetchUserEmails loads id→email for the given users (LEFT JOIN users semantics:
// a missing user maps to ""). Mirrors the legacy COALESCE(us.email,”).
func (r *usageLogRepository) fetchUserEmails(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	emails := make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return emails, nil
	}
	rows, err := r.sql.QueryContext(ctx, "SELECT id, COALESCE(email, '') FROM users WHERE id = ANY($1)", pq.Array(userIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		emails[id] = email
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return emails, nil
}

func (r *usageLogRepository) fetchUserTrendIdentity(ctx context.Context, userIDs []int64) (emails, usernames map[int64]string, err error) {
	emails = make(map[int64]string, len(userIDs))
	usernames = make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return emails, usernames, nil
	}
	rows, err := r.sql.QueryContext(
		ctx,
		"SELECT id, COALESCE(email, ''), COALESCE(username, '') FROM users WHERE id = ANY($1)",
		pq.Array(userIDs),
	)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var email, username string
		if err := rows.Scan(&id, &email, &username); err != nil {
			return nil, nil, err
		}
		emails[id] = email
		usernames[id] = username
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return emails, usernames, nil
}

// sortUserSpendingRankingItems applies the legacy ORDER BY actual_cost DESC,
// tokens DESC, user_id ASC.
func sortUserSpendingRankingItems(items []UserSpendingRankingItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.ActualCost != b.ActualCost {
			return a.ActualCost > b.ActualCost
		}
		if a.Tokens != b.Tokens {
			return a.Tokens > b.Tokens
		}
		return a.UserID < b.UserID
	})
}

func sortUserTrendTopUsers(items []UserSpendingRankingItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Tokens != b.Tokens {
			return a.Tokens > b.Tokens
		}
		return a.UserID < b.UserID
	})
}

type userTrendDayAgg struct {
	requests   int64
	tokens     int64
	cost       float64
	actualCost float64
}

// getUserUsageTrendRollup serves GetUserUsageTrend for day granularity from the
// per-(user, platform, day) rollup for completed days plus raw usage_logs for
// partial/today slices. Top-N user selection uses the same window totals as
// GetUserSpendingRanking; per-date series is rebuilt in Go to preserve legacy
// ordering (date ASC, tokens DESC).
func (r *usageLogRepository) getUserUsageTrendRollup(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	limit int,
) ([]UserUsageTrendPoint, error) {
	if limit <= 0 {
		limit = 12
	}
	dateFormat := safeDateFormat(granularity)

	byUser := make(map[int64]*userSpendAgg)
	addUserTotal := func(user int64, cost float64, reqs, toks int64) {
		a, ok := byUser[user]
		if !ok {
			a = &userSpendAgg{}
			byUser[user] = a
		}
		a.cost += cost
		a.requests += reqs
		a.tokens += toks
	}

	floorDay, hasRollupData, err := r.userPlatformRollupFloorDay(ctx)
	if err != nil {
		return nil, err
	}
	win := planUsageRollupWindow(startTime, endTime, floorDay, hasRollupData)

	if win.hasRollup {
		const q = `
			SELECT
				user_id,
				COALESCE(SUM(actual_cost), 0),
				COALESCE(SUM(total_requests), 0),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)
			FROM usage_dashboard_user_platform_daily
			WHERE bucket_date >= $1::date AND bucket_date < $2::date
			GROUP BY user_id
		`
		rows, err := r.sql.QueryContext(ctx, q, win.rollupStartDay, win.rollupEndDay)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid, reqs, toks int64
			var cost float64
			if err := rows.Scan(&uid, &cost, &reqs, &toks); err != nil {
				_ = rows.Close()
				return nil, err
			}
			addUserTotal(uid, cost, reqs, toks)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	for _, span := range win.rawSpans {
		from, to := span[0], span[1]
		const q = `
			SELECT
				user_id,
				COALESCE(SUM(actual_cost), 0),
				COUNT(*),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY user_id
		`
		rows, err := r.sql.QueryContext(ctx, q, from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid, reqs, toks int64
			var cost float64
			if err := rows.Scan(&uid, &cost, &reqs, &toks); err != nil {
				_ = rows.Close()
				return nil, err
			}
			addUserTotal(uid, cost, reqs, toks)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	rankingItems := make([]UserSpendingRankingItem, 0, len(byUser))
	for uid, a := range byUser {
		rankingItems = append(rankingItems, UserSpendingRankingItem{
			UserID:     uid,
			ActualCost: a.cost,
			Requests:   a.requests,
			Tokens:     a.tokens,
		})
	}
	sortUserTrendTopUsers(rankingItems)
	if limit > 0 && len(rankingItems) > limit {
		rankingItems = rankingItems[:limit]
	}
	if len(rankingItems) == 0 {
		return []UserUsageTrendPoint{}, nil
	}

	topUserIDs := make([]int64, len(rankingItems))
	for i, item := range rankingItems {
		topUserIDs[i] = item.UserID
	}

	type trendKey struct {
		date   string
		userID int64
	}
	byDayUser := make(map[trendKey]*userTrendDayAgg)

	addTrendRow := func(date string, userID int64, totalCost, actualCost float64, reqs, toks int64) {
		k := trendKey{date: date, userID: userID}
		a, ok := byDayUser[k]
		if !ok {
			a = &userTrendDayAgg{}
			byDayUser[k] = a
		}
		a.cost += totalCost
		a.actualCost += actualCost
		a.requests += reqs
		a.tokens += toks
	}

	if win.hasRollup {
		query := fmt.Sprintf(`
			SELECT
				TO_CHAR(bucket_date::timestamp, '%s') AS date,
				user_id,
				COALESCE(SUM(total_cost), 0),
				COALESCE(SUM(actual_cost), 0),
				COALESCE(SUM(total_requests), 0),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)
			FROM usage_dashboard_user_platform_daily
			WHERE user_id = ANY($1)
			  AND bucket_date >= $2::date AND bucket_date < $3::date
			GROUP BY bucket_date, user_id
		`, dateFormat)
		rows, err := r.sql.QueryContext(ctx, query, pq.Array(topUserIDs), win.rollupStartDay, win.rollupEndDay)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var date string
			var uid, reqs, toks int64
			var cost, actualCost float64
			if err := rows.Scan(&date, &uid, &cost, &actualCost, &reqs, &toks); err != nil {
				_ = rows.Close()
				return nil, err
			}
			addTrendRow(date, uid, cost, actualCost, reqs, toks)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	for _, span := range win.rawSpans {
		from, to := span[0], span[1]
		query := fmt.Sprintf(`
			SELECT
				TO_CHAR(created_at, '%s') AS date,
				user_id,
				COALESCE(SUM(total_cost), 0),
				COALESCE(SUM(actual_cost), 0),
				COUNT(*),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)
			FROM usage_logs
			WHERE user_id = ANY($1)
			  AND created_at >= $2 AND created_at < $3
			GROUP BY date, user_id
		`, dateFormat)
		rows, err := r.sql.QueryContext(ctx, query, pq.Array(topUserIDs), from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var date string
			var uid, reqs, toks int64
			var cost, actualCost float64
			if err := rows.Scan(&date, &uid, &cost, &actualCost, &reqs, &toks); err != nil {
				_ = rows.Close()
				return nil, err
			}
			k := trendKey{date: date, userID: uid}
			a, ok := byDayUser[k]
			if !ok {
				a = &userTrendDayAgg{}
				byDayUser[k] = a
			}
			a.cost += cost
			a.actualCost += actualCost
			a.requests += reqs
			a.tokens += toks
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	emails, usernames, err := r.fetchUserTrendIdentity(ctx, topUserIDs)
	if err != nil {
		return nil, err
	}

	results := make([]UserUsageTrendPoint, 0, len(byDayUser))
	for k, a := range byDayUser {
		results = append(results, UserUsageTrendPoint{
			Date:       k.date,
			UserID:     k.userID,
			Email:      emails[k.userID],
			Username:   usernames[k.userID],
			Requests:   a.requests,
			Tokens:     a.tokens,
			Cost:       a.cost,
			ActualCost: a.actualCost,
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Date != results[j].Date {
			return results[i].Date < results[j].Date
		}
		if results[i].Tokens != results[j].Tokens {
			return results[i].Tokens > results[j].Tokens
		}
		return results[i].UserID < results[j].UserID
	})

	return results, nil
}
