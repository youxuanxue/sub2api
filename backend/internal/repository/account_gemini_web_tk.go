package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// Only read maintenance metadata, never cookie jars or unrelated Gemini accounts.
// A poll performs no writes. Due accounts still acquire the fenced lease before
// touching Google, so concurrent workers and foreground requests remain safe.
func (r *accountRepository) ListDueGeminiWebAccounts(ctx context.Context) ([]int64, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT id,
		credentials #> '{gemini_web,runtime,state}', credentials #> '{gemini_web,lease}'
		FROM accounts WHERE deleted_at IS NULL AND platform='gemini' AND type='apikey'
		AND status='active' AND schedulable=true
		AND jsonb_typeof(credentials #> '{gemini_web,runtime}')='object'
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	now := float64(time.Now().Unix())
	for rows.Next() {
		var id int64
		var stateRaw, leaseRaw []byte
		if err := rows.Scan(&id, &stateRaw, &leaseRaw); err != nil {
			return nil, err
		}
		state, lease := map[string]any{}, map[string]any{}
		if len(stateRaw) > 0 {
			if err := json.Unmarshal(stateRaw, &state); err != nil {
				continue
			}
		}
		if len(leaseRaw) > 0 {
			if err := json.Unmarshal(leaseRaw, &lease); err != nil {
				continue
			}
		}
		lastRefresh, _ := state["last_refresh"].(float64)
		cooldown, _ := state["cooldown_until"].(float64)
		expiresAt, _ := lease["expires_at"].(float64)
		if state["blocked"] == true || state["generation_pending"] == true || cooldown > now || expiresAt > now {
			continue
		}
		if now-lastRefresh >= 600 {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// Ordinary account edits carry a redacted or stale credential snapshot. Keep the
// live session under the row lock; only an explicitly newer version can import.
// Imports cannot revoke a live worker's lease or resurrect its older cookies.
func mergeGeminiWebCredentialsLocked(ctx context.Context, client *dbent.Client, id int64, incoming map[string]any) (map[string]any, error) {
	rows, err := client.QueryContext(ctx, `SELECT credentials->'gemini_web',
		EXTRACT(EPOCH FROM clock_timestamp())::double precision FROM accounts
		WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return incoming, rows.Err()
	}
	var raw []byte
	var now float64
	if err := rows.Scan(&raw, &now); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return incoming, nil
	}
	var current map[string]any
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, err
	}
	currentRuntime, _ := current["runtime"].(map[string]any)
	encoded, err := json.Marshal(incoming["gemini_web"])
	if err != nil {
		return nil, err
	}
	var next map[string]any
	if err := json.Unmarshal(encoded, &next); err != nil {
		return nil, err
	}
	nextRuntime, _ := next["runtime"].(map[string]any)
	version, _ := currentRuntime["version"].(float64)
	nextVersion, _ := nextRuntime["version"].(float64)
	result := copyJSONMap(incoming)
	if result == nil {
		result = make(map[string]any)
	}
	if nextVersion <= version {
		result["gemini_web"] = current
		return result, nil
	}
	lease, _ := current["lease"].(map[string]any)
	expiresAt, _ := lease["expires_at"].(float64)
	if expiresAt > now {
		return nil, errors.New("gemini web session has an active operation; retry import after it completes")
	}
	if nextVersion != version+1 {
		return nil, errors.New("gemini web import must increment the current runtime version by one")
	}
	delete(next, "lease")
	result["gemini_web"] = next
	return result, nil
}

// CompareAndSwapGeminiWebRuntime replaces only credentials.gemini_web.runtime
// when the worker still owns the version it loaded. Browser session refreshes
// must never overwrite an operator's newer import.
func (r *accountRepository) CompareAndSwapGeminiWebRuntime(ctx context.Context, id int64, expectedVersion int64, owner string, runtime map[string]any) (bool, error) {
	if id <= 0 || expectedVersion < 0 {
		return false, errors.New("invalid Gemini Web runtime version")
	}
	payload, err := json.Marshal(normalizeJSONMap(runtime))
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts
		SET credentials = jsonb_set(
			COALESCE(credentials, '{}'::jsonb),
			'{gemini_web,runtime}',
			jsonb_set($1::jsonb, '{version}', to_jsonb($2::bigint + 1), true),
			true
		), updated_at = NOW()
		WHERE id = $3
			AND deleted_at IS NULL
			AND platform = 'gemini'
			AND status = 'active' AND schedulable = true
			AND credentials->'gemini_web'->'lease'->>'owner' = $4
			AND (credentials->'gemini_web'->'lease'->>'expires_at')::numeric > EXTRACT(EPOCH FROM clock_timestamp())
			AND COALESCE(credentials->'gemini_web'->'runtime'->>'version', '') = $2::text
	`, payload, expectedVersion, id, owner)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// ImportGeminiWebSession atomically replaces only the browser runtime. The
// version predicate turns a stale editor/import into a conflict and the JSONB
// expression preserves every unrelated credential and account column.
func (r *accountRepository) ImportGeminiWebSession(ctx context.Context, id, expectedVersion int64, runtime map[string]any) (bool, error) {
	if id <= 0 || expectedVersion < 0 || expectedVersion >= 1<<53-1 || runtime == nil {
		return false, errors.New("invalid Gemini Web runtime version")
	}
	payload, err := json.Marshal(normalizeJSONMap(runtime))
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts
		SET credentials = jsonb_set(
			credentials #- '{gemini_web,lease}',
			'{gemini_web,runtime}',
			jsonb_set($1::jsonb, '{version}', to_jsonb($2::bigint + 1), true),
			true
		), updated_at = NOW()
		WHERE id = $3
			AND deleted_at IS NULL
			AND platform = 'gemini' AND type = 'apikey'
			AND jsonb_typeof(credentials->'gemini_web'->'runtime') = 'object'
			AND COALESCE(credentials->'gemini_web_relay', 'false'::jsonb) = 'false'::jsonb
			AND extra->>'relay_kind' IS DISTINCT FROM 'gemini_web'
			AND COALESCE(credentials->'gemini_web'->'runtime'->>'version', '0') = $2::text
			AND COALESCE((credentials->'gemini_web'->'lease'->>'expires_at')::numeric, 0)
				<= EXTRACT(EPOCH FROM clock_timestamp())
	`, payload, expectedVersion, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// The 600s crash lease exceeds the worker's 480s total operation budget.
// CAS and lease updates serialize on the account row, without holding a DB
// connection throughout a potentially slow Google request.
func (r *accountRepository) AcquireGeminiWebLease(ctx context.Context, id int64, owner string) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts
		SET credentials = jsonb_set(credentials, '{gemini_web,lease}',
			jsonb_build_object('owner', $2::text, 'expires_at', EXTRACT(EPOCH FROM clock_timestamp()) + 600))
		WHERE id = $1 AND deleted_at IS NULL AND platform = 'gemini'
			AND type = 'apikey' AND status = 'active' AND schedulable = true
			AND jsonb_typeof(credentials->'gemini_web'->'runtime') = 'object'
			AND COALESCE((credentials->'gemini_web'->'lease'->>'expires_at')::numeric, 0)
				<= EXTRACT(EPOCH FROM clock_timestamp())`, id, owner)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *accountRepository) ReleaseGeminiWebLease(ctx context.Context, id int64, owner string) error {
	_, err := r.sql.ExecContext(ctx, `UPDATE accounts SET credentials = credentials #- '{gemini_web,lease}'
		WHERE id = $1 AND credentials->'gemini_web'->'lease'->>'owner' = $2`, id, owner)
	return err
}
