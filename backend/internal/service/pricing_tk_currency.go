package service

// tkCNYPerUSD is the TokenKey canonical conversion rate for official CNY list
// prices stored in USD-denominated pricing data.
const tkCNYPerUSD = 6.7

func tkCNYPerMTokToUSDPerToken(cny float64) float64 {
	return cny / tkCNYPerUSD / 1_000_000
}

func tkCNYPerMTokToUSDPer1KTokens(cny float64) float64 {
	return cny / tkCNYPerUSD / 1_000
}
