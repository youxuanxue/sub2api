package service

import "strings"

// settleBillingOnAccountServedModel settles token billing on the model id the
// account actually serves when account model_mapping remaps the client request.
// Serving and billing share the same mapping owner (account.GetMappedModel /
// accountModelMappingForAccount); do not duplicate alias tables in billing.
func settleBillingOnAccountServedModel(account *Account, requestedModel, billingModel string) string {
	if account == nil {
		return billingModel
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return billingModel
	}
	served := strings.TrimSpace(account.GetMappedModel(requestedModel))
	if served == "" || strings.EqualFold(served, requestedModel) {
		return billingModel
	}
	return served
}
