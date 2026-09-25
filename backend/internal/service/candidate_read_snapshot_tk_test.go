package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func TestCandidateReadSnapshotBuildPreservesStaticParityWithoutCredentials(t *testing.T) {
	group := Group{ID: 11, Status: StatusActive, Platform: PlatformOpenAI, DefaultMappedModel: "gpt-4o", ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}}
	account := Account{
		ID: 42, Platform: PlatformOpenAI,
		Credentials: map[string]any{"api_key": "must-not-be-copied", "model_mapping": map[string]any{"alias": "gpt-5.4"}},
		GroupIDs:    []int64{11}, AccountGroups: []AccountGroup{{AccountID: 42, GroupID: 11, Priority: 3}},
		ProtocolEndpointCapability: &ProtocolEndpointCapability{CapabilityKey: "cap-openai", Revision: 4, SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions}},
	}
	snapshot, err := BuildCandidateReadSnapshot([]Account{account}, []Group{group}, 7, 9, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCandidateReadSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	facts := snapshot.Accounts[42]
	if facts.CapabilityKey != "cap-openai" || facts.Platform != PlatformOpenAI {
		t.Fatalf("unexpected account facts: %#v", facts)
	}
	if facts.ModelMapping["alias"] != "gpt-5.4" || len(facts.AccountGroupIDs) != 1 || facts.AccountGroupIDs[0] != 11 {
		t.Fatalf("static account parity lost: %#v", facts)
	}
	if got := snapshot.Memberships[11]; len(got) != 1 || got[0].AccountID != 42 || got[0].Priority != 3 {
		t.Fatalf("membership parity lost: %#v", got)
	}
	if got := snapshot.Groups[11]; !got.Active || got.DirectPolicy.DefaultMappedModel != "gpt-4o" || !got.ModelAllowlist.Enabled {
		t.Fatalf("group parity lost: %#v", got)
	}
}

func TestCandidateReadSnapshotIncompleteNeverAuthorizes(t *testing.T) {
	p := newCandidateReadSnapshotProvider(nil, 0)
	incomplete := CandidateReadSnapshot{ReadRevision: 1, Complete: false}
	if err := p.PublishCandidateReadSnapshot(incomplete); !errors.Is(err, ErrCandidateReadSnapshotIncomplete) {
		t.Fatalf("publish incomplete error = %v", err)
	}
	if _, err := p.Snapshot(context.Background(), []int64{11}); !errors.Is(err, ErrCandidateReadSnapshotUnavailable) {
		t.Fatalf("empty provider error = %v", err)
	}
	complete := CandidateReadSnapshot{ReadRevision: 2, Complete: true, Groups: map[int64]CandidateGroupFacts{11: {ID: 11}}, Memberships: map[int64][]CandidateMembership{11: {}}}
	if err := p.PublishCandidateReadSnapshot(complete); err != nil {
		t.Fatal(err)
	}
	p.MarkCandidateReadSnapshotLagged()
	if _, err := p.Snapshot(context.Background(), []int64{11}); !errors.Is(err, ErrCandidateReadSnapshotLagged) {
		t.Fatalf("lagged snapshot error = %v", err)
	}
}

func TestCandidateReadSnapshotFallbackIsBoundedAndBatchScoped(t *testing.T) {
	base, err := BuildCandidateReadSnapshot(nil, []Group{{ID: 11, Status: StatusActive}}, 1, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var calls [][]int64
	fallback := CandidateReadSnapshotFallbackFunc(func(ctx context.Context, ids []int64) (CandidateReadSnapshot, error) {
		mu.Lock()
		calls = append(calls, append([]int64(nil), ids...))
		mu.Unlock()
		close(started)
		select {
		case <-release:
			return base, nil
		case <-ctx.Done():
			return CandidateReadSnapshot{}, ctx.Err()
		}
	})
	p := newCandidateReadSnapshotProvider(fallback, 1)
	// Force a miss so both requests use the single bounded fallback slot.
	firstDone := make(chan error, 1)
	go func() { _, err := p.Snapshot(context.Background(), []int64{11}); firstDone <- err }()
	<-started
	if _, err := p.Snapshot(context.Background(), []int64{11}); !errors.Is(err, ErrCandidateReadSnapshotFallbackBusy) {
		t.Fatalf("second fallback error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != 11 {
		t.Fatalf("fallback was not one bounded batch: %#v", calls)
	}
	stats := p.Stats()
	if stats.Fallbacks != 1 || stats.FallbackBusy != 1 {
		t.Fatalf("unexpected fallback stats: %#v", stats)
	}
}
