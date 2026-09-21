package service

import (
	"net/http"
	"strconv"
)

// Both Gemini entry points carry the gateway-selected account to the worker.
func setGeminiWebAccountHeader(request *http.Request, account *Account) {
	if _, ok := account.Credentials["gemini_web"].(map[string]any); ok {
		request.Header.Set("X-TokenKey-Gemini-Web-Account-ID", strconv.FormatInt(account.ID, 10))
	}
}
