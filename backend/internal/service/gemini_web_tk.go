package service

import (
	"net/http"
	"strconv"
	"strings"
)

// Gateway and admin tests carry their selected local account to the Worker.
func setGeminiWebAccountHeader(request *http.Request, account *Account) {
	if geminiWebRuntimeBound(account) {
		request.Header.Set("X-TokenKey-Gemini-Web-Account-ID", strconv.FormatInt(account.ID, 10))
	}
}

// geminiWebMissingAccountReferenceMarker is the Worker message for a missing or
// malformed X-TokenKey-Gemini-Web-Account-ID. Current Workers emit HTTP 400;
// older builds still use 401 — both are request-shape faults, never cookie
// revocation.
const geminiWebMissingAccountReferenceMarker = "missing gemini web account reference"

func geminiWebMissingAccountReferenceMessage(upstreamMsg string, body []byte) bool {
	hay := strings.ToLower(strings.TrimSpace(upstreamMsg))
	if hay == "" {
		hay = strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	}
	return strings.Contains(hay, geminiWebMissingAccountReferenceMarker)
}

// tkIsGeminiWebMissingAccountReference401 reports whether a 401 from a Gemini
// Web account is the missing account-binding fault (gateway omitted the Worker
// account header) rather than an auth failure that should SetError.
func tkIsGeminiWebMissingAccountReference401(account *Account, statusCode int, upstreamMsg string, body []byte) bool {
	if statusCode != http.StatusUnauthorized || !isGeminiWebAccount(account) {
		return false
	}
	return geminiWebMissingAccountReferenceMessage(upstreamMsg, body)
}

// geminiWebMissingAccountReferenceClientFault is true when upstream rejected for
// a missing Worker account binding. Callers must surface HTTP 400 to the client
// and must not failover or SetError — the same construction error would hit every
// candidate account.
func geminiWebMissingAccountReferenceClientFault(account *Account, statusCode int, body []byte) bool {
	if account == nil || !isGeminiWebAccount(account) {
		return false
	}
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnauthorized {
		return false
	}
	return geminiWebMissingAccountReferenceMessage("", body)
}
