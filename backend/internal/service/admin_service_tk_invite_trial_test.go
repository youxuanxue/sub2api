//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// trialSettingRepoStub is a minimal in-memory SettingRepository for trial-preset
// + frontend-url reads/writes.
type trialSettingRepoStub struct {
	values map[string]string
}

func newTrialSettingRepoStub() *trialSettingRepoStub {
	return &trialSettingRepoStub{values: map[string]string{}}
}

func (s *trialSettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}
func (s *trialSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}
func (s *trialSettingRepoStub) Set(ctx context.Context, key, value string) error {
	s.values[key] = value
	return nil
}
func (s *trialSettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s *trialSettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	for k, v := range settings {
		s.values[k] = v
	}
	return nil
}
func (s *trialSettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	return s.values, nil
}
func (s *trialSettingRepoStub) Delete(ctx context.Context, key string) error {
	delete(s.values, key)
	return nil
}

// trialGroupReaderStub implements DefaultSubscriptionGroupReader.
type trialGroupReaderStub struct {
	groups map[int64]*Group
}

func (r *trialGroupReaderStub) GetByID(ctx context.Context, id int64) (*Group, error) {
	if g, ok := r.groups[id]; ok {
		return g, nil
	}
	return nil, ErrGroupNotFound
}

func newTrialSettingService(reader DefaultSubscriptionGroupReader) (*SettingService, *trialSettingRepoStub) {
	repo := newTrialSettingRepoStub()
	svc := NewSettingService(repo, &config.Config{})
	if reader != nil {
		svc.SetDefaultSubscriptionGroupReader(reader)
	}
	return svc, repo
}

func subGroup(id int64, name string) *Group {
	return &Group{ID: id, Name: name, SubscriptionType: SubscriptionTypeSubscription, Status: StatusActive}
}

func TestTrialPresets_SetGetRoundTrip(t *testing.T) {
	reader := &trialGroupReaderStub{groups: map[int64]*Group{7: subGroup(7, "trial-7")}}
	svc, _ := newTrialSettingService(reader)
	ctx := context.Background()

	rate := 1.5
	err := svc.SetTrialPresets(ctx, []TrialPreset{
		{Name: "小圈子7天", GroupID: 7, ValidityDays: 7, Balance: 5, Concurrency: 2, RPMLimit: 0, Rate: &rate},
	})
	require.NoError(t, err)

	got := svc.GetTrialPresets(ctx)
	require.Len(t, got, 1)
	require.Equal(t, "小圈子7天", got[0].Name)
	require.Equal(t, int64(7), got[0].GroupID)
	require.Equal(t, 7, got[0].ValidityDays)
	require.NotNil(t, got[0].Rate)
	require.Equal(t, 1.5, *got[0].Rate)
}

func TestTrialPresets_RejectNonSubscriptionGroup(t *testing.T) {
	// Group exists but is not subscription-type.
	reader := &trialGroupReaderStub{groups: map[int64]*Group{9: {ID: 9, Name: "plain", SubscriptionType: ""}}}
	svc, _ := newTrialSettingService(reader)

	err := svc.SetTrialPresets(context.Background(), []TrialPreset{{Name: "bad", GroupID: 9, ValidityDays: 30}})
	require.Error(t, err)
}

func TestTrialPresets_RejectDuplicateName(t *testing.T) {
	reader := &trialGroupReaderStub{groups: map[int64]*Group{7: subGroup(7, "g7")}}
	svc, _ := newTrialSettingService(reader)

	err := svc.SetTrialPresets(context.Background(), []TrialPreset{
		{Name: "dup", GroupID: 7, ValidityDays: 30},
		{Name: "dup", GroupID: 7, ValidityDays: 30},
	})
	require.Error(t, err)
}

func TestTrialPresets_ValidityClampAndDefault(t *testing.T) {
	require.Equal(t, 30, normalizeTrialPreset(TrialPreset{Name: "x", GroupID: 1, ValidityDays: 0}).ValidityDays)
	require.Equal(t, MaxValidityDays, normalizeTrialPreset(TrialPreset{Name: "x", GroupID: 1, ValidityDays: MaxValidityDays + 99}).ValidityDays)
}

func TestParseTrialPresets_DropsInvalidRows(t *testing.T) {
	raw := `[{"name":"ok","group_id":3,"validity_days":7},{"name":"","group_id":5},{"name":"nogroup","group_id":0}]`
	got := parseTrialPresets(raw)
	require.Len(t, got, 1)
	require.Equal(t, "ok", got[0].Name)
}

func TestBuildTrialCard_ExactFormat(t *testing.T) {
	card := buildTrialCard("https://api.tokenkey.dev/home", "wxj@tk.com", "2bN296SrU2MwEZDX")
	require.Equal(t, "平台 https://api.tokenkey.dev/home\n账号 wxj@tk.com\n密码 2bN296SrU2MwEZDX", card)
}

func TestBuildTrialCard_OmitsEmptyHome(t *testing.T) {
	card := buildTrialCard("", "a@b.com", "pw")
	require.Equal(t, "账号 a@b.com\n密码 pw", card)
}

func TestFrontendHost_DropsApiPrefix(t *testing.T) {
	require.Equal(t, "tokenkey.dev", frontendHost("https://api.tokenkey.dev"))
	require.Equal(t, "tokenkey.dev", frontendHost("api.tokenkey.dev/home"))
	require.Equal(t, "example.com", frontendHost("http://example.com:8080"))
	require.Equal(t, "", frontendHost(""))
}

func TestGenerateTrialPassword_LengthAndCharset(t *testing.T) {
	pw := generateTrialPassword(16)
	require.Len(t, pw, 16)
	const allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for _, c := range pw {
		require.True(t, strings.ContainsRune(allowed, c), "unexpected char %q", c)
	}
	// Two draws should not be identical (crypto-random; collision negligible).
	require.NotEqual(t, pw, generateTrialPassword(16))
}

func TestResolvePlan_FromPreset(t *testing.T) {
	reader := &trialGroupReaderStub{groups: map[int64]*Group{7: subGroup(7, "g7")}}
	settingSvc, _ := newTrialSettingService(reader)
	ctx := context.Background()
	require.NoError(t, settingSvc.SetTrialPresets(ctx, []TrialPreset{
		{Name: "p1", GroupID: 7, ValidityDays: 14, Balance: 3, Concurrency: 2},
	}))

	ps := NewTrialProvisionService(nil, nil, settingSvc, nil, nil, nil)
	plan, err := ps.resolvePlan(ctx, &ProvisionTrialInput{PresetName: "p1"})
	require.NoError(t, err)
	require.Equal(t, int64(7), plan.GroupID)
	require.Equal(t, 14, plan.ValidityDays)
	require.Equal(t, 3.0, plan.Balance)
	require.Equal(t, 2, plan.Concurrency)
}

func TestResolvePlan_UnknownPreset(t *testing.T) {
	settingSvc, _ := newTrialSettingService(nil)
	ps := NewTrialProvisionService(nil, nil, settingSvc, nil, nil, nil)
	_, err := ps.resolvePlan(context.Background(), &ProvisionTrialInput{PresetName: "missing"})
	require.Error(t, err)
}

func TestResolvePlan_InlineDefaultsConcurrencyAndValidity(t *testing.T) {
	settingSvc, _ := newTrialSettingService(nil)
	ps := NewTrialProvisionService(nil, nil, settingSvc, nil, nil, nil)
	plan, err := ps.resolvePlan(context.Background(), &ProvisionTrialInput{
		Plan: TrialPlan{GroupID: 4},
	})
	require.NoError(t, err)
	require.Equal(t, 30, plan.ValidityDays) // default
	require.GreaterOrEqual(t, plan.Concurrency, 1)
}

func TestBuildRecipients_AutoCountAndCap(t *testing.T) {
	ps := NewTrialProvisionService(nil, nil, nil, nil, nil, nil)

	rs, err := ps.buildRecipients(&ProvisionTrialInput{AutoCount: 3})
	require.NoError(t, err)
	require.Len(t, rs, 3)

	rs, err = ps.buildRecipients(&ProvisionTrialInput{
		Recipients: []TrialRecipient{{Email: "a@b.com"}, {Email: "  "}},
		AutoCount:  2,
	})
	require.NoError(t, err)
	require.Len(t, rs, 3) // one explicit + two auto; blank dropped

	_, err = ps.buildRecipients(&ProvisionTrialInput{})
	require.Error(t, err) // none

	_, err = ps.buildRecipients(&ProvisionTrialInput{AutoCount: 201})
	require.Error(t, err) // over cap
}
