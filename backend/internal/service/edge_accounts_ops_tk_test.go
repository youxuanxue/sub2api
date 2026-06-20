//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// opCaptureDoer records the single forwarded request so the test can assert the
// method, path, query, body, and x-api-key the prod proxy sends to the edge.
type opCaptureDoer struct {
	req      *http.Request
	body     string
	status   int
	respBody string
	err      error
}

func (d *opCaptureDoer) Do(req *http.Request) (*http.Response, error) {
	d.req = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		d.body = string(b)
	}
	if d.err != nil {
		return nil, d.err
	}
	st := d.status
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{
		StatusCode: st,
		Body:       io.NopCloser(strings.NewReader(d.respBody)),
		Header:     make(http.Header),
	}, nil
}

func opsAggregator(doer httpDoer) *EdgeAccountsAggregator {
	store := &edgeAccountsStoreStub{accounts: []Account{
		mirrorStub(1, "https://api-us4.tokenkey.dev", "key-us4"),
	}}
	return NewEdgeAccountsAggregator(store, doer)
}

func TestForwardAccountOp_UnsupportedOpRejected(t *testing.T) {
	doer := &opCaptureDoer{}
	agg := opsAggregator(doer)
	_, _, err := agg.ForwardAccountOp(context.Background(), "us4", 51, EdgeAccountOp("delete-everything"), "", nil)
	require.ErrorIs(t, err, ErrUnsupportedEdgeAccountOp)
	require.Nil(t, doer.req, "must not reach the edge for an unwhitelisted op")
}

func TestForwardAccountOp_UnknownEdgeNotFound(t *testing.T) {
	doer := &opCaptureDoer{}
	agg := opsAggregator(doer)
	_, _, err := agg.ForwardAccountOp(context.Background(), "zz9", 51, EdgeAccountOpClearRateLimit, "", nil)
	require.ErrorIs(t, err, ErrEdgeNotFound)
	require.Nil(t, doer.req)
}

func TestForwardAccountOp_BuildsPathAndForwardsKey(t *testing.T) {
	doer := &opCaptureDoer{status: http.StatusOK, respBody: `{"code":0,"data":{"id":51}}`}
	agg := opsAggregator(doer)

	status, body, err := agg.ForwardAccountOp(context.Background(), "us4", 51, EdgeAccountOpClearRateLimit, "", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"code":0,"data":{"id":51}}`, string(body))

	require.NotNil(t, doer.req)
	require.Equal(t, http.MethodPost, doer.req.Method)
	require.Equal(t, "/api/v1/edge/accounts/51/clear-rate-limit", doer.req.URL.Path)
	require.Equal(t, "api-us4.tokenkey.dev", doer.req.URL.Host)
	require.Equal(t, "key-us4", doer.req.Header.Get("x-api-key"))
}

func TestForwardAccountOp_UsageAppendsSanitizedQuery(t *testing.T) {
	doer := &opCaptureDoer{respBody: `{"code":0,"data":{}}`}
	agg := opsAggregator(doer)
	_, _, err := agg.ForwardAccountOp(context.Background(), "us4", 51, EdgeAccountOpUsage, "source=active&force=true", nil)
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, doer.req.Method)
	require.Equal(t, "/api/v1/edge/accounts/51/usage", doer.req.URL.Path)
	require.Equal(t, "source=active&force=true", doer.req.URL.RawQuery)
}

func TestForwardAccountOp_SchedulableForwardsBody(t *testing.T) {
	doer := &opCaptureDoer{respBody: `{"code":0,"data":{}}`}
	agg := opsAggregator(doer)
	_, _, err := agg.ForwardAccountOp(context.Background(), "us4", 51, EdgeAccountOpSetSchedulable, "", []byte(`{"schedulable":false}`))
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, doer.req.Method)
	require.Equal(t, "/api/v1/edge/accounts/51/schedulable", doer.req.URL.Path)
	require.JSONEq(t, `{"schedulable":false}`, doer.body)
	require.Equal(t, "application/json", doer.req.Header.Get("Content-Type"))
}

func TestForwardAccountOp_RelaysEdgeNon2xxVerbatim(t *testing.T) {
	doer := &opCaptureDoer{status: http.StatusConflict, respBody: `{"code":1,"message":"nope"}`}
	agg := opsAggregator(doer)
	status, body, err := agg.ForwardAccountOp(context.Background(), "us4", 51, EdgeAccountOpResetQuota, "", nil)
	require.NoError(t, err) // transport ok → caller relays the edge's own status/body
	require.Equal(t, http.StatusConflict, status)
	require.JSONEq(t, `{"code":1,"message":"nope"}`, string(body))
}

func TestForwardAccountOp_TransportErrorBubbles(t *testing.T) {
	doer := &opCaptureDoer{err: errors.New("connection refused")}
	agg := opsAggregator(doer)
	status, _, err := agg.ForwardAccountOp(context.Background(), "us4", 51, EdgeAccountOpClearRateLimit, "", nil)
	require.Error(t, err)
	require.Equal(t, 0, status)
}

func TestForwardAccountOp_BadLocalIDRejected(t *testing.T) {
	doer := &opCaptureDoer{}
	agg := opsAggregator(doer)
	_, _, err := agg.ForwardAccountOp(context.Background(), "us4", 0, EdgeAccountOpClearRateLimit, "", nil)
	require.Error(t, err)
	require.Nil(t, doer.req)
}

func TestMirrorStubEdgeID(t *testing.T) {
	// A real anthropic mirror stub → derived edge id.
	require.Equal(t, "us4", MirrorStubEdgeID(accPtr(mirrorStub(1, "https://api-us4.tokenkey.dev", "k"))))
	// Trailing slash still matches.
	require.Equal(t, "uk2", MirrorStubEdgeID(accPtr(mirrorStub(2, "https://api-uk2.tokenkey.dev/", "k"))))
	// OAuth account (not apikey) is not a mirror stub.
	oauth := Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": "https://api-us4.tokenkey.dev"}}
	require.Equal(t, "", MirrorStubEdgeID(&oauth))
	// base_url not an internal edge → not a stub.
	require.Equal(t, "", MirrorStubEdgeID(accPtr(mirrorStub(3, "https://example.com", "k"))))
	// nil is safe.
	require.Equal(t, "", MirrorStubEdgeID(nil))
}

func accPtr(a Account) *Account { return &a }
