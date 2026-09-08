package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const supplierCredentialFaultPrefix = "Supplier credential failure: "

// SupplierCredentialFaultStore updates runtime state only while the inspected
// credentials and error still match. It never changes configured capacity.
type SupplierCredentialFaultStore interface {
	ListSupplierCredentialFaultAccounts(context.Context) ([]Account, error)
	UpdateSupplierCredentialFault(context.Context, *Account, string) (bool, error)
}

func IsSupplierCredentialFault(message string) bool {
	return strings.HasPrefix(message, supplierCredentialFaultPrefix)
}

func supplierCredentialIdentity(account *Account, fingerprinter SupplierCredentialFingerprinter) (string, string, error) {
	endpoint, err := NormalizeSupplierEndpoint(account.GetCredential("base_url"))
	if err != nil {
		return "", "", err
	}
	fingerprint, err := fingerprinter.Fingerprint(account.GetCredential("api_key"))
	return endpoint, fingerprint, err
}

func (s *RateLimitService) supplierCredentialPeers(ctx context.Context, account *Account) ([]Account, error) {
	store, ok := s.accountRepo.(SupplierCredentialFaultStore)
	if !ok || !supplierManagedTransportOK(account) || !HasSupplierManagedTransportIdentity(account) {
		return nil, nil
	}
	fingerprinter := NewSupplierCredentialFingerprinter(s.cfg)
	endpoint, fingerprint, err := supplierCredentialIdentity(account, fingerprinter)
	if err != nil {
		return nil, err
	}
	accounts, err := store.ListSupplierCredentialFaultAccounts(ctx)
	if err != nil {
		return nil, err
	}
	peers := make([]Account, 0)
	foundManagedOrigin := false
	for i := range accounts {
		peer := &accounts[i]
		if !supplierManagedTransportOK(peer) {
			continue
		}
		if _, managed := supplierSourceIDFromAccount(peer); !managed {
			continue
		}
		foundManagedOrigin = foundManagedOrigin || peer.ID == account.ID
		peerEndpoint, peerFingerprint, identityErr := supplierCredentialIdentity(peer, fingerprinter)
		if identityErr == nil && endpoint == peerEndpoint && hmacEqualString(fingerprint, peerFingerprint) {
			peers = append(peers, *peer)
		}
	}
	if !foundManagedOrigin {
		return nil, nil
	}
	return peers, nil
}

func (s *RateLimitService) tkTryHandleSupplierCredentialFailure(ctx context.Context, account *Account, reason, message string) bool {
	if s == nil || account == nil {
		return false
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	peers, err := s.supplierCredentialPeers(stateCtx, account)
	if err != nil {
		slog.Warn("supplier_credential_failure_lookup_failed", "account_id", account.ID, "error", err)
		return false
	}
	if peers == nil {
		return false
	}
	// A stale request must not disable freshly rotated credentials.
	foundOrigin := false
	for _, peer := range peers {
		foundOrigin = foundOrigin || peer.ID == account.ID
	}
	if !foundOrigin {
		return true
	}
	store, ok := s.accountRepo.(SupplierCredentialFaultStore)
	if !ok {
		return false
	}
	errorMessage := supplierCredentialFaultPrefix + message
	for i := range peers {
		peer := &peers[i]
		if peer.Status != StatusActive && (peer.Status != StatusError || !IsSupplierCredentialFault(peer.ErrorMessage)) {
			continue
		}
		updated, updateErr := store.UpdateSupplierCredentialFault(stateCtx, peer, errorMessage)
		if updateErr != nil {
			slog.Warn("supplier_credential_failure_write_failed", "account_id", peer.ID, "error", updateErr)
			// Stop immediate retries even when durable state is temporarily unavailable.
			s.notifyAccountSchedulingBlocked(peer, time.Time{}, reason, message)
			continue
		}
		if updated {
			s.notifyAccountSchedulingBlocked(peer, time.Time{}, reason, message)
		}
	}
	return true
}

// RecoverSupplierCredentialPeers clears only this owner's error marker. It
// preserves each projection's model limits, protocol state and manual pause.
func (s *RateLimitService) RecoverSupplierCredentialPeers(ctx context.Context, account *Account) error {
	if s == nil || account == nil || !IsSupplierCredentialFault(account.ErrorMessage) {
		return nil
	}
	peers, err := s.supplierCredentialPeers(ctx, account)
	if err != nil {
		return fmt.Errorf("load supplier credential recovery peers: %w", err)
	}
	if len(peers) == 0 {
		return nil
	}
	store, ok := s.accountRepo.(SupplierCredentialFaultStore)
	if !ok {
		return nil
	}
	for i := range peers {
		peer := &peers[i]
		if peer.ID == account.ID || peer.Status != StatusError || !IsSupplierCredentialFault(peer.ErrorMessage) {
			continue
		}
		updated, updateErr := store.UpdateSupplierCredentialFault(ctx, peer, "")
		if updateErr != nil {
			return fmt.Errorf("recover supplier account %d: %w", peer.ID, updateErr)
		}
		if updated {
			s.notifyAccountSchedulingBlockCleared(peer.ID)
		}
	}
	return nil
}

func tkIsConfirmedSupplierCredentialAuthError(statusCode int, body []byte) bool {
	if (statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden) || isHTMLResponse(body) ||
		tkIsCapabilityScope401(statusCode, body) || IsOpenAICompatModelNotFound404(body, "") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(body))) {
	case "invalid_api_key", "invalid_token", "api_key_invalid", "token_revoked", "token_invalidated", "invalid_authentication":
		return true
	}
	message := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	for _, marker := range []string{"invalid api key", "incorrect api key", "invalid x-api-key", "api key is invalid", "api key has been revoked", "invalid access token"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
