package repository

import (
	"encoding/json"
	"fmt"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountEntityConversionKeepsJSONMapsIndependent(t *testing.T) {
	for _, convert := range []struct {
		name string
		fn   func(*dbent.Account) *service.Account
	}{{"full", accountEntityToService}, {"supplier", supplierAccountEntityToService}} {
		t.Run(convert.name, func(t *testing.T) {
			entity := &dbent.Account{
				Credentials: map[string]any{"api_key": "original", "model_mapping": map[string]any{"public": "upstream"}},
				Extra:       map[string]any{"base_rpm": float64(10)},
			}
			first, second := convert.fn(entity), convert.fn(entity)
			first.Credentials["api_key"] = "rotated"
			delete(first.Credentials, "model_mapping")
			first.Extra["base_rpm"] = float64(20)
			require.Equal(t, "original", entity.Credentials["api_key"])
			require.Equal(t, entity.Credentials, second.Credentials)
			require.Equal(t, map[string]any{"base_rpm": float64(10)}, entity.Extra)
			require.Equal(t, entity.Extra, second.Extra)
			entity.Credentials["api_key"] = "later"
			require.Equal(t, "original", second.Credentials["api_key"])
			for _, source := range []map[string]any{nil, {}} {
				out := convert.fn(&dbent.Account{Credentials: source, Extra: source})
				require.Equal(t, source, out.Credentials)
				require.Equal(t, source, out.Extra)
			}
		})
	}
}

var materializedAccountSink *service.Account

// Separates map materialization from full JSON decoding. This excludes database
// IO and relation loading; it is not an end-to-end selection benchmark.
func BenchmarkAccountLoadMaterialization(b *testing.B) {
	for _, fields := range []int{8, 32, 128} {
		b.Run(fmt.Sprintf("fields_%d", fields), func(b *testing.B) {
			entity := &dbent.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Credentials: make(map[string]any), Extra: make(map[string]any)}
			for i := 0; i < fields; i++ {
				entity.Credentials[fmt.Sprintf("credential_%d", i)] = "synthetic-value"
				entity.Extra[fmt.Sprintf("extra_%d", i)] = float64(i)
			}
			mapping := make(map[string]any, 128)
			for i := 0; i < 128; i++ {
				mapping[fmt.Sprintf("public-%d", i)] = fmt.Sprintf("upstream-%d", i)
			}
			entity.Credentials["model_mapping"] = mapping
			credentials, err := json.Marshal(entity.Credentials)
			require.NoError(b, err)
			extra, err := json.Marshal(entity.Extra)
			require.NoError(b, err)
			b.Run("convert", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					materializedAccountSink = accountEntityToService(entity)
				}
			})
			b.Run("decode_and_convert", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					row := *entity
					row.Credentials, row.Extra = nil, nil
					if err := json.Unmarshal(credentials, &row.Credentials); err != nil {
						b.Fatal(err)
					}
					if err := json.Unmarshal(extra, &row.Extra); err != nil {
						b.Fatal(err)
					}
					materializedAccountSink = accountEntityToService(&row)
				}
			})
		})
	}
}
