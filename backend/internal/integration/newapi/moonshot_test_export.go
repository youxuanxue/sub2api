package newapi

// SetMoonshotProbeBasesForTest exposes the package-private
// moonshotProbeBasesForTest sentinel to other test packages (specifically
// service-layer tests for resolveNewAPIMoonshotBaseURLOnSave) that need to
// inject httptest URLs without poking at private symbols. Pass nil to
// restore the official cn/ai bases.
//
// Production callers MUST NOT use this. It exists in a non-_test.go file so
// the symbol is visible to other test packages, but the function name
// includes "ForTest" to keep the contract obvious in code review.
func SetMoonshotProbeBasesForTest(bases []string) {
	moonshotProbeBasesForTest = bases
}
