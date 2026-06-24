package service

// Prod-side WRITE proxy for edge-local accounts: the write-direction sibling of
// the read aggregator (edge_accounts_aggregator_tk.go). It forwards a WHITELISTED
// account op to the target edge's least-privilege ops endpoint
// (handler.EdgeAccountOpsHandler / POST|DELETE|GET /api/v1/edge/accounts/:id/<op>)
// using the SAME discovery (resolveTarget), the SAME mirror-stub x-api-key auth,
// the SAME per-call timeout and per-edge failure isolation as fetchEdgeAccounts /
// MintAdminSession.
//
// Boundaries (mirroring the read path's doctrine):
//   - The op is an enum, never user free text — prod can never be coerced into
//     forwarding to an arbitrary edge path (the path is built from a fixed spec).
//   - Stateless: no edge admin JWT is minted or cached here; the credential-free
//     mirror-stub api-key prod already holds is the only secret (zero new secret),
//     and the bounded blast radius is exactly the whitelisted ops.
//   - Credentials never traverse this path: every whitelisted op is status-class
//     (cooldown / quota / schedulable / usage); credential-class ops stay on the
//     edge via the admin-session handoff.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// EdgeAccountOp identifies a whitelisted edge account write/query op. The
// aggregator maps it to a FIXED (method, path-suffix) pair, so the forwarded path
// is never derived from caller-supplied free text.
type EdgeAccountOp string

const (
	EdgeAccountOpClearRateLimit   EdgeAccountOp = "clear-rate-limit"
	EdgeAccountOpResetQuota       EdgeAccountOp = "reset-quota"
	EdgeAccountOpClearTempUnsched EdgeAccountOp = "temp-unschedulable"
	EdgeAccountOpSetSchedulable   EdgeAccountOp = "schedulable"
	EdgeAccountOpUsage            EdgeAccountOp = "usage"
)

// edgeAccountOpSpec is the fixed method + path-suffix for one whitelisted op.
type edgeAccountOpSpec struct {
	method string
	suffix string // appended after .../accounts/<id>/
}

// edgeAccountOpSpecs is the immutable whitelist. A request for any op not present
// here is rejected before any edge call — the single source of truth for what prod
// is allowed to forward.
var edgeAccountOpSpecs = map[EdgeAccountOp]edgeAccountOpSpec{
	EdgeAccountOpClearRateLimit:   {http.MethodPost, "clear-rate-limit"},
	EdgeAccountOpResetQuota:       {http.MethodPost, "reset-quota"},
	EdgeAccountOpClearTempUnsched: {http.MethodDelete, "temp-unschedulable"},
	EdgeAccountOpSetSchedulable:   {http.MethodPost, "schedulable"},
	EdgeAccountOpUsage:            {http.MethodGet, "usage"},
}

// ErrUnsupportedEdgeAccountOp is returned when an op is not in the whitelist.
var ErrUnsupportedEdgeAccountOp = errors.New("unsupported edge account op")

// ForwardAccountOp resolves the edge by id and forwards the whitelisted op to
// <base_url>/api/v1/edge/accounts/<localID>/<suffix> with the mirror-stub
// x-api-key. localID is the EDGE-local account id (the edge's own DB primary key).
// rawQuery (already sanitized by the caller) is appended for the usage GET; body
// (already validated JSON) is sent for the schedulable POST.
//
// Returns the edge's HTTP status code + raw response body so the prod handler can
// relay it verbatim — the body is the edge's credential-free DTO / usage / error
// envelope and prod adds nothing. A statusCode of 0 with a non-nil error means the
// edge was unreachable / the request could not be built (the handler maps that to
// 502); ErrEdgeNotFound means the edge id did not resolve (→ 404).
func (a *EdgeAccountsAggregator) ForwardAccountOp(ctx context.Context, edgeID string, localID int64, op EdgeAccountOp, rawQuery string, body []byte) (int, []byte, error) {
	spec, ok := edgeAccountOpSpecs[op]
	if !ok {
		return 0, nil, ErrUnsupportedEdgeAccountOp
	}
	if localID <= 0 {
		return 0, nil, errors.New("invalid account id")
	}

	t, err := a.resolveTarget(ctx, edgeID)
	if err != nil {
		return 0, nil, err // ErrEdgeNotFound bubbles up → handler maps to 404
	}
	if a.http == nil {
		return 0, nil, errors.New("no http client")
	}

	endpoint := t.baseURL + "/api/v1/edge/accounts/" + strconv.FormatInt(localID, 10) + "/" + spec.suffix
	if spec.method == http.MethodGet && rawQuery != "" {
		endpoint += "?" + rawQuery
	}

	reqCtx, cancel := context.WithTimeout(ctx, edgeAccountsHTTPTO)
	defer cancel()

	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, spec.method, endpoint, reqBody)
	if err != nil {
		return 0, nil, errors.New("build request failed")
	}
	req.Header.Set("x-api-key", t.apiKey)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, errors.New("request failed: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap — single-account responses are tiny
	if err != nil {
		return resp.StatusCode, nil, errors.New("read body failed")
	}
	return resp.StatusCode, respBody, nil
}

// SyncEdgeStubGroup mirrors a prod mirror-stub account's single group binding to
// the edge-side relay API key used by that stub. The cross-deployment join key is
// the group NAME, never the group id (prod and edge DB ids can differ). Best-effort
// callers should log failures but not roll back the already-saved prod edit.
func (a *EdgeAccountsAggregator) SyncEdgeStubGroup(ctx context.Context, account *Account) error {
	if a == nil || account == nil {
		return nil
	}
	targets := discoverStubTargets([]Account{*account}, edgeIDPattern)
	if len(targets) != 1 {
		return nil
	}
	groupNames := stubGroupNames(account)
	if len(groupNames) > 1 {
		return nil
	}
	groupName := ""
	if len(groupNames) == 1 {
		groupName = groupNames[0]
	}
	if a.http == nil {
		return errors.New("no http client")
	}

	payload, err := json.Marshal(map[string]string{"group_name": groupName})
	if err != nil {
		return err
	}
	t := targets[0]
	endpoint := t.baseURL + "/api/v1/edge/caller-api-key/group"

	reqCtx, cancel := context.WithTimeout(ctx, edgeAccountsHTTPTO)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("build request failed")
	}
	req.Header.Set("x-api-key", t.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return errors.New("request failed: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = "edge returned http " + strconv.Itoa(resp.StatusCode)
		}
		return errors.New(msg)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
