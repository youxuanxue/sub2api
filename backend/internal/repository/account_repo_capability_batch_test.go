package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

var capabilityBatchColumns = []string{
	"account_ids", "id", "capability_key", "identity", "supported_protocols", "probe_evidence", "revision",
	"last_probed_at", "probe_lease_owner", "probe_lease_until", "probe_generation",
	"identity_conflict", "created_at", "updated_at", "linked_account_count",
}

func capabilityBatchRow(accountID, capabilityID int64, evidence string) []driver.Value {
	now := time.Date(2026, time.September, 24, 0, 0, 0, 0, time.UTC)
	return []driver.Value{
		fmt.Sprintf("{%d}", accountID), capabilityID, fmt.Sprintf("capability-%d", capabilityID),
		`{"key_schema_version":1,"platform":"openai","endpoint_profile":"custom_api_key","channel_type":"openai","protocol_endpoints":{"responses":{"url":"https://relay.example.test/v1/responses","api_version":""}},"upstream_request_profile":"openai_json_v1","routing_headers":{"x-routing":"original"}}`,
		`["responses"]`, evidence, int64(7), now, "probe-owner", now, int64(3), false, now, now, int64(64),
	}
}

func capabilityBatchEvidence(models int) string {
	capabilities := make(map[string]any, models)
	for i := 0; i < models; i++ {
		capabilities[fmt.Sprintf("fixture-model-%d", i)] = map[string]any{"responses": map[string]bool{"always_thinking": true}}
	}
	raw, err := json.Marshal(map[string]any{
		"initial_probe_completed": true, "model_capabilities": capabilities,
		"verdicts": map[string]any{"responses": map[string]any{"supported": true, "checks": []any{map[string]any{"detail": strings.Repeat("synthetic evidence ", 16)}}}},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// Measures database row scanning and capability materialization, excluding SQL
// server/network time and fixture construction. Integration tests own SQL semantics.
func BenchmarkLoadAccountCapabilityBatch(b *testing.B) {
	for _, tc := range []struct {
		name                string
		accounts, endpoints int
	}{
		{"single", 1, 1}, {"shared_64", 64, 1}, {"distinct_64", 64, 64},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ids := make([]int64, tc.accounts)
			for i := range ids {
				ids[i] = int64(i + 1)
			}
			evidence := capabilityBatchEvidence(32)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				db, mock, err := sqlmock.New()
				if err != nil {
					b.Fatal(err)
				}
				rows := sqlmock.NewRows(capabilityBatchColumns)
				for endpoint := 0; endpoint < tc.endpoints; endpoint++ {
					var linked []string
					for j, id := range ids {
						if j%tc.endpoints == endpoint {
							linked = append(linked, fmt.Sprint(id))
						}
					}
					row := capabilityBatchRow(0, int64(endpoint+1), evidence)
					row[0] = "{" + strings.Join(linked, ",") + "}"
					rows.AddRow(row...)
				}
				mock.ExpectQuery(`(?s)SELECT.*linked_account_count`).WithArgs(sqlmock.AnyArg()).WillReturnRows(rows)
				repo := &accountRepository{sql: db}
				b.StartTimer()
				got, err := repo.loadProtocolEndpointCapabilities(context.Background(), ids)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if len(got) != len(ids) {
					b.Fatal("missing linked account capabilities")
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					b.Fatal(err)
				}
				if err := db.Close(); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}

func TestLoadAccountCapabilitiesAreIndependentAndFresh(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &accountRepository{sql: db}
	evidence := capabilityBatchEvidence(1)
	shared := capabilityBatchRow(41, 9, evidence)
	shared[0] = "{41,42}"
	rows := sqlmock.NewRows(capabilityBatchColumns).AddRow(shared...)
	mock.ExpectQuery(`(?s)SELECT.*linked_account_count`).WithArgs(sqlmock.AnyArg()).WillReturnRows(rows)
	got, err := repo.loadProtocolEndpointCapabilities(context.Background(), []int64{41, 42})
	require.NoError(t, err)
	require.Equal(t, got[41], got[42])
	original := *got[42].LastProbedAt
	got[41].Identity.RoutingHeaders["x-routing"] = "changed"
	delete(got[41].Identity.ProtocolEndpoints, "responses")
	got[41].SupportedProtocols[0] = "messages"
	delete(got[41].ProbeEvidence.ModelCapabilities["fixture-model-0"], "responses")
	protocolEvidenceFirstCheck(t, got[41].ProbeEvidence.Verdicts)["detail"] = "changed"
	*got[41].LastProbedAt = time.Time{}
	*got[41].ProbeLeaseOwner = "changed"
	*got[41].ProbeLeaseUntil = time.Time{}
	require.Equal(t, "original", got[42].Identity.RoutingHeaders["x-routing"])
	require.Equal(t, "https://relay.example.test/v1/responses", got[42].Identity.ProtocolEndpoints["responses"].URL)
	require.Equal(t, "responses", string(got[42].SupportedProtocols[0]))
	require.True(t, got[42].ProbeEvidence.ModelCapabilities["fixture-model-0"]["responses"].AlwaysThinking)
	require.Equal(t, strings.Repeat("synthetic evidence ", 16), protocolEvidenceFirstCheck(t, got[42].ProbeEvidence.Verdicts)["detail"])
	require.Equal(t, original, *got[42].LastProbedAt)
	require.Equal(t, "probe-owner", *got[42].ProbeLeaseOwner)
	require.Equal(t, original, *got[42].ProbeLeaseUntil)
	// A subsequent load must observe a new revision and account link, not reuse
	// a cross-request decoded snapshot or a mutation of the preceding result.
	updated := capabilityBatchRow(41, 10, evidence)
	updated[6] = int64(8)
	mock.ExpectQuery(`(?s)SELECT.*linked_account_count`).WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows(capabilityBatchColumns).AddRow(updated...))
	fresh, err := repo.loadProtocolEndpointCapabilities(context.Background(), []int64{41, 42})
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.EqualValues(t, 10, fresh[41].ID)
	require.EqualValues(t, 8, fresh[41].Revision)
	require.Equal(t, "original", fresh[41].Identity.RoutingHeaders["x-routing"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLoadAccountCapabilityBatchErrorsRemainVisible(t *testing.T) {
	for _, tc := range []struct {
		name   string
		column int
		value  driver.Value
		want   string
	}{
		{"bad account ids", 0, "{not-an-id}", "invalid syntax"},
		{"bad identity", 3, `{"platform":123}`, "decode protocol endpoint identity"},
		{"bad protocols", 4, `{}`, "decode supported protocols"},
		{"bad evidence", 5, `{"model_capabilities":42}`, "decode protocol probe evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			row := capabilityBatchRow(41, 9, capabilityBatchEvidence(1))
			row[tc.column] = tc.value
			mock.ExpectQuery(`(?s)SELECT.*linked_account_count`).WithArgs(sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows(capabilityBatchColumns).AddRow(row...)).RowsWillBeClosed()
			repo := &accountRepository{sql: db}
			got, err := repo.loadProtocolEndpointCapabilities(context.Background(), []int64{41})
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, got, "decode failure must not publish a partial eligibility snapshot")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
	for _, kind := range []string{"query", "iteration"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			want := fmt.Errorf("fixture %s failure", kind)
			expected := mock.ExpectQuery(`(?s)SELECT.*linked_account_count`).WithArgs(sqlmock.AnyArg())
			if kind == "query" {
				expected.WillReturnError(want)
			} else {
				expected.WillReturnRows(sqlmock.NewRows(capabilityBatchColumns).AddRow(capabilityBatchRow(41, 9, `{}`)...).RowError(0, want)).RowsWillBeClosed()
			}
			repo := &accountRepository{sql: db}
			_, err = repo.loadProtocolEndpointCapabilities(context.Background(), []int64{41})
			require.ErrorIs(t, err, want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestLoadAccountCapabilityBatchPreservesNullFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	row := capabilityBatchRow(41, 9, `{"model_capabilities":{"fixture":null},"verdicts":{"nested":null,"empty":[]}}`)
	row[0] = "{41,42}"
	row[3] = `{"routing_headers":null,"protocol_endpoints":null}`
	row[4] = `[]`
	row[7] = nil
	row[8] = nil
	row[9] = nil
	mock.ExpectQuery(`(?s)SELECT.*linked_account_count`).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(capabilityBatchColumns).AddRow(row...))
	got, err := (&accountRepository{sql: db}).loadProtocolEndpointCapabilities(context.Background(), []int64{41, 42})
	require.NoError(t, err)
	require.Equal(t, got[41], got[42])
	require.Nil(t, got[41].LastProbedAt)
	require.Nil(t, got[41].Identity.RoutingHeaders)
	require.Nil(t, got[41].ProbeEvidence.ModelCapabilities["fixture"])
	require.Nil(t, got[41].ProbeEvidence.Verdicts["nested"])
	require.Equal(t, []any{}, got[41].ProbeEvidence.Verdicts["empty"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func protocolEvidenceFirstCheck(t *testing.T, verdicts map[string]any) map[string]any {
	t.Helper()
	response, ok := verdicts["responses"].(map[string]any)
	require.True(t, ok)
	checks, ok := response["checks"].([]any)
	require.True(t, ok)
	require.Len(t, checks, 1)
	check, ok := checks[0].(map[string]any)
	require.True(t, ok)
	return check
}
