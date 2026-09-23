package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type geminiImportRepo struct {
	AccountRepository
	applied bool
	reads   int
}

func (r *geminiImportRepo) ImportGeminiWebSession(context.Context, int64, int64, map[string]any) (bool, error) {
	return r.applied, nil
}
func (r *geminiImportRepo) GetByID(context.Context, int64) (*Account, error) {
	r.reads++
	return &Account{ID: 28}, nil
}

func TestGeminiWebImportServiceReportsCASConflict(t *testing.T) {
	r := &geminiImportRepo{}
	s := &adminServiceImpl{accountRepo: r}
	a, err := s.ImportGeminiWebSession(context.Background(), 28, 7, map[string]any{})
	require.ErrorContains(t, err, "session changed")
	require.Nil(t, a)
	require.Zero(t, r.reads, "a rejected write cannot become a successful read response")
	r.applied = true
	a, err = s.ImportGeminiWebSession(context.Background(), 28, 7, map[string]any{})
	require.NoError(t, err)
	require.EqualValues(t, 28, a.ID)
}
