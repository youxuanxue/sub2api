package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const MachineAdminKeyPrefix = "sk-tkm-"
const machineAdminSettingPrefix = "tk_machine_admin_key_"
const AuditAuthMethodMachineAdminKey = "machine_admin_key"

var ErrInvalidMachineAdminKey = infraerrors.Unauthorized("INVALID_ADMIN_KEY", "Invalid admin API key")

// MachineAdminKey is safe to list. Verification material belongs exclusively to
// machineAdminKeyRecord; neither the secret nor its digest is an API field.
type MachineAdminKey struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerUserID int64     `json:"owner_user_id"`
	Scopes      []string  `json:"scopes"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type machineAdminKeyRecord struct {
	MachineAdminKey
	Digest string `json:"digest"`
}

func validMachineAdminID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == id
}

func (s *SettingService) CreateMachineAdminKey(ctx context.Context, ownerUserID int64, name string, scopes []string, ttlHours int) (*MachineAdminKey, string, error) {
	name = strings.TrimSpace(name)
	if ownerUserID <= 0 || name == "" || len(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) || ttlHours < 1 || ttlHours > 2160 {
		return nil, "", infraerrors.BadRequest("INVALID_MACHINE_KEY", "A name and a lifetime of 1 to 2160 hours are required")
	}
	if len(scopes) == 0 || len(scopes) > len(machineAdminPermissions) {
		return nil, "", infraerrors.BadRequest("INVALID_MACHINE_SCOPES", "Select explicit machine permissions")
	}
	scopes = slices.Clone(scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	for _, scope := range scopes {
		if !validMachineAdminScope(scope) {
			return nil, "", infraerrors.BadRequest("INVALID_MACHINE_SCOPES", "Unknown machine permission")
		}
	}
	var entropy [48]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, "", fmt.Errorf("generate machine admin key: %w", err)
	}
	id := hex.EncodeToString(entropy[:16])
	token := MachineAdminKeyPrefix + id + "_" + hex.EncodeToString(entropy[16:])
	digest := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	record := machineAdminKeyRecord{
		MachineAdminKey: MachineAdminKey{ID: id, Name: name, OwnerUserID: ownerUserID, Scopes: scopes, CreatedAt: now, ExpiresAt: now.Add(time.Duration(ttlHours) * time.Hour)},
		Digest:          hex.EncodeToString(digest[:]),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, "", err
	}
	// One independently addressed row per immutable credential; concurrent
	// creation/revocation cannot overwrite a shared read-modify-write key list.
	if err := s.settingRepo.Set(ctx, machineAdminSettingPrefix+id, string(encoded)); err != nil {
		return nil, "", err
	}
	return &record.MachineAdminKey, token, nil
}

func (s *SettingService) AuthenticateMachineAdminKey(ctx context.Context, token string) (*MachineAdminKey, error) {
	parts := strings.Split(strings.TrimPrefix(token, MachineAdminKeyPrefix), "_")
	if !strings.HasPrefix(token, MachineAdminKeyPrefix) || len(parts) != 2 || !validMachineAdminID(parts[0]) || len(parts[1]) != 64 {
		return nil, ErrInvalidMachineAdminKey
	}
	// Intentionally uncached: revocation takes effect on the next request on
	// every app instance sharing this database. An in-flight operation can finish.
	encoded, err := s.settingRepo.GetValue(ctx, machineAdminSettingPrefix+parts[0])
	if errors.Is(err, ErrSettingNotFound) {
		return nil, ErrInvalidMachineAdminKey
	}
	if err != nil {
		return nil, err
	}
	var record machineAdminKeyRecord
	if err := json.Unmarshal([]byte(encoded), &record); err != nil {
		return nil, ErrInvalidMachineAdminKey
	}
	digest := sha256.Sum256([]byte(token))
	stored, err := hex.DecodeString(record.Digest)
	if err != nil || subtle.ConstantTimeCompare(digest[:], stored) != 1 || record.ID != parts[0] || record.OwnerUserID <= 0 || !time.Now().Before(record.ExpiresAt) || len(record.Scopes) == 0 {
		return nil, ErrInvalidMachineAdminKey
	}
	for _, scope := range record.Scopes {
		if !validMachineAdminScope(scope) {
			return nil, ErrInvalidMachineAdminKey
		}
	}
	return &record.MachineAdminKey, nil
}

func (s *SettingService) ListMachineAdminKeys(ctx context.Context) ([]MachineAdminKey, error) {
	values, err := s.settingRepo.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]MachineAdminKey, 0)
	for key, value := range values {
		if !strings.HasPrefix(key, machineAdminSettingPrefix) {
			continue
		}
		var record machineAdminKeyRecord
		if err := json.Unmarshal([]byte(value), &record); err != nil {
			return nil, errors.New("invalid stored machine credential metadata")
		}
		keys = append(keys, record.MachineAdminKey)
	}
	slices.SortFunc(keys, func(a, b MachineAdminKey) int { return strings.Compare(a.ID, b.ID) })
	return keys, nil
}

func (s *SettingService) RevokeMachineAdminKey(ctx context.Context, id string) error {
	if !validMachineAdminID(id) {
		return infraerrors.BadRequest("INVALID_MACHINE_KEY_ID", "Invalid machine credential ID")
	}
	return s.settingRepo.Delete(ctx, machineAdminSettingPrefix+id)
}
