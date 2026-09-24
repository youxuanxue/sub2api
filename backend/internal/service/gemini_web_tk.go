package service

import (
	"net/http"
	"strconv"
)

// Gateway and admin tests carry their selected local account to the Worker.
func setGeminiWebAccountHeader(request *http.Request, account *Account) {
	if geminiWebRuntimeBound(account) {
		request.Header.Set("X-TokenKey-Gemini-Web-Account-ID", strconv.FormatInt(account.ID, 10))
	}
}
