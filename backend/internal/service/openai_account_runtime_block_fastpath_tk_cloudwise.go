package service

// tkCloudwiseSkipPermanentRuntimeBlock is true when CloudWise model-balance 402
// or Provider error 424 already owns a model-scoped / bounded temp-unsched path,
// so the generic permanent in-memory BlockAccountScheduling must be skipped.
func tkCloudwiseSkipPermanentRuntimeBlock(
	account *Account,
	statusCode int,
	responseBody []byte,
	canonicalModel []string,
) bool {
	cloudwiseModelBalanceMatched := len(canonicalModel) > 0 &&
		tkIsCloudwiseModelBalance402(account, statusCode, responseBody, canonicalModel[0])
	cloudwiseProvider424Matched := tkIsCloudwiseProvider424Response(account, statusCode, responseBody)
	return cloudwiseModelBalanceMatched || cloudwiseProvider424Matched
}
