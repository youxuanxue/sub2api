package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUS048_DiscountBandBoundaries(t *testing.T) {
	ratio0199, ratio02, ratio0399 := 0.199, 0.2, 0.399
	ratio04, ratio0599, ratio06 := 0.4, 0.599, 0.6
	ratio0799, ratio08, ratio0999, ratio1 := 0.799, 0.8, 0.999, 1.0

	tests := []struct {
		name  string
		ratio *float64
		want  int
	}{
		{name: "below point two", ratio: &ratio0199, want: 1},
		{name: "point two boundary", ratio: &ratio02, want: 2},
		{name: "below point four", ratio: &ratio0399, want: 2},
		{name: "point four boundary", ratio: &ratio04, want: 3},
		{name: "below point six", ratio: &ratio0599, want: 3},
		{name: "point six boundary", ratio: &ratio06, want: 4},
		{name: "below point eight", ratio: &ratio0799, want: 4},
		{name: "point eight boundary", ratio: &ratio08, want: 5},
		{name: "below one", ratio: &ratio0999, want: 5},
		{name: "one", ratio: &ratio1, want: 6},
		{name: "unspecified", ratio: nil, want: 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SupplierDiscountBandForRatio(tt.ratio)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestUS048_DiscountBandRejectsOutOfRangeRatios(t *testing.T) {
	zero, negative, aboveOne := 0.0, -0.1, 1.01
	for _, ratio := range []*float64{&zero, &negative, &aboveOne} {
		_, err := SupplierDiscountBandForRatio(ratio)
		require.ErrorIs(t, err, ErrSupplierSourceInvalidPurchaseRatio)
	}
}

func TestUS048_SupplierPriorityIsBasePlusBand(t *testing.T) {
	got, err := SupplierAccountPriority(100, 3)
	require.NoError(t, err)
	require.Equal(t, 103, got)

	_, err = SupplierAccountPriority(100, 0)
	require.ErrorIs(t, err, ErrSupplierSourceInvalidInput)
}

func TestUS048_SupplierPriorityStaysWithinPostgresIntegerRange(t *testing.T) {
	got, err := SupplierAccountPriority((1<<31)-7, 6)
	require.NoError(t, err)
	require.Equal(t, (1<<31)-1, got)

	for _, basePriority := range []int{(1 << 31) - 6, -(1 << 31) - 1} {
		_, err := SupplierAccountPriority(basePriority, 6)
		require.ErrorIs(t, err, ErrSupplierSourceInvalidInput)

		input := SupplierSourceInput{
			SupplierName: "supplier", ChannelName: "channel", ChannelType: 1, Endpoint: "https://supplier.example/v1",
			BasePriority: &basePriority,
		}
		require.ErrorIs(t, input.Validate(), ErrSupplierSourceInvalidInput)
	}
}

func TestUS048_SupplierSourceInputRejectsDuplicateClientModel(t *testing.T) {
	ratio := 0.5
	input := SupplierSourceInput{
		SupplierName: "佳杰",
		ChannelName:  "stbl-5",
		ChannelType:  1, Endpoint: "https://token.vstecscloud.com/v1/",
		Models: []SupplierSourceModelInput{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
			{ClientModelID: " deepseek-v4-pro ", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
		},
	}

	err := input.Validate()
	require.ErrorIs(t, err, ErrSupplierSourceDuplicateClientModel)
}

func TestUS048_SupplierSourceInputRequiresExplicitUpstreamModel(t *testing.T) {
	ratio := 0.5
	for _, upstreamModelID := range []string{"", "*", "全系列"} {
		input := SupplierSourceInput{
			SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://token.vstecscloud.com/v1",
			Models: []SupplierSourceModelInput{{
				ClientModelID: "deepseek-v4-pro", UpstreamModelID: upstreamModelID, PurchaseRatio: &ratio,
			}},
		}

		require.ErrorIs(t, input.Validate(), ErrSupplierSourceInvalidInput)
	}
}

func TestUS048_SupplierSourceInputRejectsNonFinitePurchaseRatio(t *testing.T) {
	for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		input := SupplierSourceInput{
			SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://token.vstecscloud.com/v1",
			Models: []SupplierSourceModelInput{{
				ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio,
			}},
		}

		require.ErrorIs(t, input.Validate(), ErrSupplierSourceInvalidPurchaseRatio)
	}
}
