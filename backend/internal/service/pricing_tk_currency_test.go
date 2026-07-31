//go:build unit

package service

// Test-only conversion helper for independent expected values. Runtime prices
// are resolved from the embedded registry and never converted in Go.
const tkCNYPerUSD = 6.7

func tkCNYPerMTokToUSDPerToken(cny float64) float64 {
	return cny / tkCNYPerUSD / 1_000_000
}
