package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestUS048_ListSupplierUpstreamModelsUsesAliCompatibleModePath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, "Bearer ali-secret", r.Header.Get("Authorization"))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "qwen3.7-max", "type": "chat"}},
		}))
	}))
	defer server.Close()

	svc := &AccountTestService{httpUpstream: &supplierUpstreamModelsHTTPFake{server: server}}
	entries, err := svc.ListSupplierUpstreamModels(
		context.Background(),
		server.URL,
		newapiconstant.ChannelTypeAli,
		"ali-secret",
	)
	require.NoError(t, err)
	require.Equal(t, "/compatible-mode/v1/models", gotPath)
	require.Equal(t, []SupplierUpstreamModelEntry{{ID: "qwen3.7-max", Type: "chat"}}, entries)
}

type supplierUpstreamModelsHTTPFake struct {
	server *httptest.Server
}

func (f *supplierUpstreamModelsHTTPFake) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

func (f *supplierUpstreamModelsHTTPFake) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxyURL, accountID, concurrency)
}

func TestUS048_SupplierSourceInputRejectsUnknownChannelType(t *testing.T) {
	ratio := 0.5
	input := SupplierSourceInput{
		SupplierName: "ali", ChannelName: "default", ChannelType: 99999,
		Endpoint: "https://dashscope.aliyuncs.com", Credential: "secret",
		Models: []SupplierSourceModelInput{{
			ClientModelID: "qwen3.7-max", UpstreamModelID: "qwen3.7-max", PurchaseRatio: &ratio,
		}},
	}
	require.ErrorIs(t, input.Validate(), errSupplierSourceInvalidChannelType)
}
