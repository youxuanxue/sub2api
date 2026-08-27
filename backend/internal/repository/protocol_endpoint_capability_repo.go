package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type protocolCapabilitySQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type protocolCapabilityBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

type protocolEndpointCapabilityRepository struct {
	db protocolCapabilitySQL
}

func NewProtocolEndpointCapabilityRepository(db *sql.DB) service.ProtocolEndpointCapabilityRepository {
	return newProtocolEndpointCapabilityRepositoryWithDB(db)
}

func newProtocolEndpointCapabilityRepositoryWithDB(db protocolCapabilitySQL) *protocolEndpointCapabilityRepository {
	return &protocolEndpointCapabilityRepository{db: db}
}

type protocolCapabilityTx struct {
	db       protocolCapabilitySQL
	commit   func() error
	rollback func() error
}

func (r *protocolEndpointCapabilityRepository) begin(ctx context.Context) (protocolCapabilityTx, error) {
	if r == nil || r.db == nil {
		return protocolCapabilityTx{}, errors.New("protocol endpoint capability database is required")
	}
	if beginner, ok := r.db.(protocolCapabilityBeginner); ok {
		tx, err := beginner.BeginTx(ctx, nil)
		if err != nil {
			return protocolCapabilityTx{}, err
		}
		return protocolCapabilityTx{db: tx, commit: tx.Commit, rollback: tx.Rollback}, nil
	}
	return protocolCapabilityTx{db: r.db, commit: func() error { return nil }, rollback: func() error { return nil }}, nil
}

func (r *protocolEndpointCapabilityRepository) EnsureAccountLink(
	ctx context.Context,
	account *service.Account,
	identity service.ProtocolEndpointIdentity,
	historicalPositive []protocolrouter.Protocol,
	officialSeed bool,
) (_ *service.ProtocolEndpointCapability, retErr error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			_ = tx.rollback()
		}
	}()
	capability, err := ensureProtocolEndpointCapabilityLink(
		ctx,
		tx.db,
		account,
		identity,
		historicalPositive,
		officialSeed,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.commit(); err != nil {
		return nil, err
	}
	return capability, nil
}

func ensureProtocolEndpointCapabilityLink(
	ctx context.Context,
	db protocolCapabilitySQL,
	account *service.Account,
	identity service.ProtocolEndpointIdentity,
	historicalPositive []protocolrouter.Protocol,
	officialSeed bool,
) (*service.ProtocolEndpointCapability, error) {
	if db == nil {
		return nil, errors.New("protocol endpoint capability database is required")
	}
	if account == nil || account.ID <= 0 {
		return nil, errors.New("account id must be positive")
	}
	key := identity.Key()
	if key == "" {
		return nil, errors.New("protocol endpoint capability identity is invalid")
	}
	identityJSON, err := identity.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	normalizedHistorical, err := service.NormalizeSupportedProtocols(historicalPositive)
	if err != nil {
		return nil, err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO protocol_endpoint_capabilities (capability_key, identity, supported_protocols, probe_evidence)
VALUES ($1, $2::jsonb, '[]'::jsonb, '{}'::jsonb)
ON CONFLICT (capability_key) DO NOTHING`, key, string(identityJSON))
	if err != nil {
		return nil, err
	}

	capability, err := queryProtocolCapability(ctx, db, `
SELECT id, capability_key, identity, supported_protocols, probe_evidence, revision,
       last_probed_at, probe_lease_owner, probe_lease_until, probe_generation,
       identity_conflict, created_at, updated_at
FROM protocol_endpoint_capabilities
WHERE capability_key=$1
FOR UPDATE`, key)
	if err != nil {
		return nil, err
	}
	storedIdentityJSON, err := capability.Identity.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	if string(storedIdentityJSON) != string(identityJSON) {
		return nil, errors.New("capability key collision or identity mismatch")
	}

	merged, err := mergeProtocolSets(capability.SupportedProtocols, normalizedHistorical)
	if err != nil {
		return nil, err
	}
	evidence := capability.ProbeEvidence
	if officialSeed {
		evidence.OfficialSeed = true
		evidence.InitialProbeCompleted = true
	}
	protocolsChanged := !protocolSlicesEqual(capability.SupportedProtocols, merged)
	evidenceChanged := !reflect.DeepEqual(evidence, capability.ProbeEvidence)
	if protocolsChanged || evidenceChanged {
		protocolJSON, marshalErr := marshalProtocols(merged)
		if marshalErr != nil {
			return nil, marshalErr
		}
		evidenceJSON, marshalErr := json.Marshal(evidence)
		if marshalErr != nil {
			return nil, marshalErr
		}
		revisionDelta := 0
		if protocolsChanged {
			revisionDelta = 1
		}
		_, err = db.ExecContext(ctx, `
UPDATE protocol_endpoint_capabilities
SET supported_protocols=$2::jsonb,
    probe_evidence=$3::jsonb,
    revision=revision+$4,
    updated_at=NOW()
WHERE id=$1`, capability.ID, string(protocolJSON), string(evidenceJSON), revisionDelta)
		if err != nil {
			return nil, err
		}
		capability.SupportedProtocols = merged
		capability.ProbeEvidence = evidence
		capability.Revision += int64(revisionDelta)
	}

	_, err = db.ExecContext(ctx, `
UPDATE accounts
SET protocol_endpoint_capability_id=$2, updated_at=NOW()
WHERE id=$1 AND deleted_at IS NULL`, account.ID, capability.ID)
	if err != nil {
		return nil, err
	}
	if err := writeRollbackProjections(ctx, db, capability.ID, capability.SupportedProtocols); err != nil {
		return nil, err
	}
	linked, err := countProtocolCapabilityLinkedAccounts(ctx, db, capability.ID)
	if err != nil {
		return nil, err
	}
	if protocolsChanged || evidenceChanged {
		linkedAccountIDs, listErr := listProtocolCapabilityLinkedAccountIDs(ctx, db, capability.ID)
		if listErr != nil {
			return nil, listErr
		}
		for _, linkedAccountID := range linkedAccountIDs {
			accountID := linkedAccountID
			if err := enqueueSchedulerOutbox(ctx, db, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
				return nil, err
			}
		}
	}
	capability.LinkedAccountCount = linked
	account.ProtocolEndpointCapabilityID = &capability.ID
	account.ProtocolEndpointCapability = capability
	return capability, nil
}

func unlinkProtocolEndpointCapability(ctx context.Context, db protocolCapabilitySQL, account *service.Account) error {
	if db == nil {
		return errors.New("protocol endpoint capability database is required")
	}
	if account == nil || account.ID <= 0 {
		return errors.New("account id must be positive")
	}
	_, err := db.ExecContext(ctx, `
UPDATE accounts
SET protocol_endpoint_capability_id=NULL,
    extra=jsonb_set(COALESCE(extra, '{}'::jsonb), '{supported_protocols}', '[]'::jsonb, true),
    updated_at=NOW()
WHERE id=$1 AND deleted_at IS NULL`, account.ID)
	if err != nil {
		return err
	}
	account.ProtocolEndpointCapabilityID = nil
	account.ProtocolEndpointCapability = nil
	return nil
}

func (r *protocolEndpointCapabilityRepository) GetByAccountID(ctx context.Context, accountID int64) (*service.ProtocolEndpointCapability, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("protocol endpoint capability database is required")
	}
	return queryProtocolCapability(ctx, r.db, `
SELECT c.id, c.capability_key, c.identity, c.supported_protocols, c.probe_evidence, c.revision,
       c.last_probed_at, c.probe_lease_owner, c.probe_lease_until, c.probe_generation,
       c.identity_conflict, c.created_at, c.updated_at
FROM accounts a
JOIN protocol_endpoint_capabilities c ON c.id=a.protocol_endpoint_capability_id
WHERE a.id=$1 AND a.deleted_at IS NULL`, accountID)
}

func (r *protocolEndpointCapabilityRepository) GetByKey(ctx context.Context, capabilityKey string) (*service.ProtocolEndpointCapability, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("protocol endpoint capability database is required")
	}
	return queryProtocolCapability(ctx, r.db, `
SELECT id, capability_key, identity, supported_protocols, probe_evidence, revision,
       last_probed_at, probe_lease_owner, probe_lease_until, probe_generation,
       identity_conflict, created_at, updated_at
FROM protocol_endpoint_capabilities WHERE capability_key=$1`, capabilityKey)
}

func (r *protocolEndpointCapabilityRepository) ListLinkedAccountIDs(ctx context.Context, capabilityKey string) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT a.id
FROM accounts a
JOIN protocol_endpoint_capabilities c ON c.id=a.protocol_endpoint_capability_id
WHERE c.capability_key=$1 AND a.deleted_at IS NULL
ORDER BY a.id`, capabilityKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *protocolEndpointCapabilityRepository) AcquireProbeLease(
	ctx context.Context,
	capabilityKey, owner string,
	now time.Time,
	ttl time.Duration,
) (service.ProtocolProbeLease, bool, error) {
	if capabilityKey == "" || owner == "" || ttl <= 0 {
		return service.ProtocolProbeLease{}, false, errors.New("capability key, lease owner, and positive ttl are required")
	}
	var lease service.ProtocolProbeLease
	rows, err := r.db.QueryContext(ctx, `
UPDATE protocol_endpoint_capabilities
SET probe_generation=probe_generation+1,
    probe_lease_owner=$2,
    probe_lease_until=$3,
    updated_at=NOW()
WHERE capability_key=$1
  AND (probe_lease_until IS NULL OR probe_lease_until <= $4)
RETURNING capability_key, probe_generation, revision, probe_lease_owner`,
		capabilityKey, owner, now.Add(ttl), now,
	)
	if err != nil {
		return service.ProtocolProbeLease{}, false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return service.ProtocolProbeLease{}, false, err
		}
		return service.ProtocolProbeLease{}, false, nil
	}
	if err := rows.Scan(&lease.CapabilityKey, &lease.Generation, &lease.Revision, &lease.Owner); err != nil {
		return service.ProtocolProbeLease{}, false, err
	}
	return lease, true, nil
}

func (r *protocolEndpointCapabilityRepository) CommitProbeResult(
	ctx context.Context,
	lease service.ProtocolProbeLease,
	mutation service.ProtocolCapabilityMutation,
) (_ *service.ProtocolEndpointCapability, _ int, retErr error) {
	normalized, err := service.NormalizeSupportedProtocols(mutation.SupportedProtocols)
	if err != nil {
		return nil, 0, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if retErr != nil {
			_ = tx.rollback()
		}
	}()
	current, err := queryProtocolCapability(ctx, tx.db, `
SELECT id, capability_key, identity, supported_protocols, probe_evidence, revision,
       last_probed_at, probe_lease_owner, probe_lease_until, probe_generation,
       identity_conflict, created_at, updated_at
FROM protocol_endpoint_capabilities
WHERE capability_key=$1
FOR UPDATE`, lease.CapabilityKey)
	if err != nil {
		return nil, 0, err
	}
	if current.Revision != lease.Revision || current.ProbeGeneration != lease.Generation ||
		current.ProbeLeaseOwner == nil || *current.ProbeLeaseOwner != lease.Owner {
		return nil, 0, service.ErrProtocolCapabilityStaleWrite
	}
	evidence := mutation.ProbeEvidence
	if evidence.Verdicts == nil {
		evidence.Verdicts = current.ProbeEvidence.Verdicts
	}
	evidence.OfficialSeed = current.ProbeEvidence.OfficialSeed
	evidence.InitialProbeCompleted = mutation.InitialProbeCompleted || current.ProbeEvidence.InitialProbeCompleted
	evidence.IdentityConflict = mutation.IdentityConflict
	changed := !protocolSlicesEqual(current.SupportedProtocols, normalized) || current.IdentityConflict != mutation.IdentityConflict
	revision := current.Revision
	if changed {
		revision++
	}
	protocolJSON, err := marshalProtocols(normalized)
	if err != nil {
		return nil, 0, err
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return nil, 0, err
	}
	lastProbedAt := mutation.LastProbedAt
	if lastProbedAt.IsZero() {
		lastProbedAt = time.Now().UTC()
	}
	result, err := tx.db.ExecContext(ctx, `
UPDATE protocol_endpoint_capabilities
SET supported_protocols=$2::jsonb,
    probe_evidence=$3::jsonb,
    revision=$4,
    last_probed_at=$5,
    identity_conflict=$6,
    probe_lease_owner=NULL,
    probe_lease_until=NULL,
    updated_at=NOW()
WHERE id=$1 AND revision=$7 AND probe_generation=$8 AND probe_lease_owner=$9`,
		current.ID, string(protocolJSON), string(evidenceJSON), revision, lastProbedAt,
		mutation.IdentityConflict, lease.Revision, lease.Generation, lease.Owner)
	if err != nil {
		return nil, 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	if affected != 1 {
		return nil, 0, service.ErrProtocolCapabilityStaleWrite
	}
	if err := writeRollbackProjections(ctx, tx.db, current.ID, normalized); err != nil {
		return nil, 0, err
	}
	linkedAccountIDs, err := listProtocolCapabilityLinkedAccountIDs(ctx, tx.db, current.ID)
	if err != nil {
		return nil, 0, err
	}
	for _, accountID := range linkedAccountIDs {
		accountID := accountID
		if err := enqueueSchedulerOutbox(ctx, tx.db, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			return nil, 0, err
		}
	}
	if err := tx.commit(); err != nil {
		return nil, 0, err
	}
	current.SupportedProtocols = normalized
	current.ProbeEvidence = evidence
	current.Revision = revision
	current.LastProbedAt = &lastProbedAt
	current.IdentityConflict = mutation.IdentityConflict
	current.ProbeLeaseOwner = nil
	current.ProbeLeaseUntil = nil
	current.LinkedAccountCount = len(linkedAccountIDs)
	return current, len(linkedAccountIDs), nil
}

func listProtocolCapabilityLinkedAccountIDs(ctx context.Context, db protocolCapabilitySQL, capabilityID int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM accounts WHERE protocol_endpoint_capability_id=$1 AND deleted_at IS NULL ORDER BY id`, capabilityID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func countProtocolCapabilityLinkedAccounts(ctx context.Context, db protocolCapabilitySQL, capabilityID int64) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT COUNT(*) FROM accounts WHERE protocol_endpoint_capability_id=$1 AND deleted_at IS NULL`, capabilityID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, sql.ErrNoRows
	}
	var linked int
	if err := rows.Scan(&linked); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return linked, nil
}

func writeRollbackProjections(ctx context.Context, db protocolCapabilitySQL, capabilityID int64, protocols []protocolrouter.Protocol) error {
	encoded, err := marshalProtocols(protocols)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
UPDATE accounts
SET extra=jsonb_set(COALESCE(extra, '{}'::jsonb), '{supported_protocols}', $2::jsonb, true),
    updated_at=NOW()
WHERE protocol_endpoint_capability_id=$1 AND deleted_at IS NULL`, capabilityID, string(encoded))
	return err
}

func mergeProtocolSets(left, right []protocolrouter.Protocol) ([]protocolrouter.Protocol, error) {
	all := append(append([]protocolrouter.Protocol(nil), left...), right...)
	return service.NormalizeSupportedProtocols(all)
}

func protocolSlicesEqual(left, right []protocolrouter.Protocol) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func marshalProtocols(protocols []protocolrouter.Protocol) ([]byte, error) {
	normalized, err := service.NormalizeSupportedProtocols(protocols)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(normalized))
	for i, protocol := range normalized {
		values[i] = string(protocol)
	}
	return json.Marshal(values)
}

type protocolCapabilityRowScanner interface {
	Scan(...any) error
}

func scanProtocolCapability(row protocolCapabilityRowScanner) (*service.ProtocolEndpointCapability, error) {
	state := newProtocolCapabilityScanState()
	err := row.Scan(state.destinations()...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrProtocolCapabilityNotFound
	}
	if err != nil {
		return nil, err
	}
	return state.decode()
}

func queryProtocolCapability(ctx context.Context, db protocolCapabilitySQL, query string, args ...any) (*service.ProtocolEndpointCapability, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrProtocolCapabilityNotFound
	}
	return scanProtocolCapability(rows)
}

type protocolCapabilityScanState struct {
	capability    service.ProtocolEndpointCapability
	identityJSON  []byte
	protocolsJSON []byte
	evidenceJSON  []byte
}

func newProtocolCapabilityScanState() *protocolCapabilityScanState {
	return &protocolCapabilityScanState{}
}

func (s *protocolCapabilityScanState) destinations() []any {
	return []any{
		&s.capability.ID,
		&s.capability.CapabilityKey,
		&s.identityJSON,
		&s.protocolsJSON,
		&s.evidenceJSON,
		&s.capability.Revision,
		&s.capability.LastProbedAt,
		&s.capability.ProbeLeaseOwner,
		&s.capability.ProbeLeaseUntil,
		&s.capability.ProbeGeneration,
		&s.capability.IdentityConflict,
		&s.capability.CreatedAt,
		&s.capability.UpdatedAt,
	}
}

func (s *protocolCapabilityScanState) decode() (*service.ProtocolEndpointCapability, error) {
	if err := json.Unmarshal(s.identityJSON, &s.capability.Identity); err != nil {
		return nil, fmt.Errorf("decode protocol endpoint identity: %w", err)
	}
	var protocolValues []string
	if err := json.Unmarshal(s.protocolsJSON, &protocolValues); err != nil {
		return nil, fmt.Errorf("decode supported protocols: %w", err)
	}
	protocols := make([]protocolrouter.Protocol, 0, len(protocolValues))
	for _, value := range protocolValues {
		protocols = append(protocols, protocolrouter.Protocol(value))
	}
	var err error
	s.capability.SupportedProtocols, err = service.NormalizeSupportedProtocols(protocols)
	if err != nil {
		return nil, err
	}
	if len(s.evidenceJSON) > 0 {
		if err := json.Unmarshal(s.evidenceJSON, &s.capability.ProbeEvidence); err != nil {
			return nil, fmt.Errorf("decode protocol probe evidence: %w", err)
		}
	}
	return &s.capability, nil
}
