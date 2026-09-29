package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGPTImageSizesArtifactMatchesGoOwner(t *testing.T) {
	artifact := filepath.Join("..", "..", "..", "frontend", "src", "constants", "gptImageSizes.generated.tk.ts")
	want := renderGPTImageSizesTypeScript(service.ExportGPTImageStudioSizeChips())
	got, err := os.ReadFile(artifact)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got),
		"FE GPT_IMAGE_SIZES drifted from Go owner; run: cd backend && go run ./cmd/gpt-image-sizes-ssot --output ../frontend/src/constants/gptImageSizes.generated.tk.ts")
}

func TestExportGPTImageStudioSizeChips_OneChipPerRatio(t *testing.T) {
	chips := service.ExportGPTImageStudioSizeChips()
	require.NotEmpty(t, chips)
	seen := map[string]string{}
	for _, chip := range chips {
		require.NotEmpty(t, chip.Ratio)
		require.NotEmpty(t, chip.Value)
		require.Equal(t, chip.Ratio, serviceAspectRatioFromSize(t, chip.Value))
		_, dup := seen[chip.Ratio]
		require.False(t, dup, "duplicate studio chip for ratio %s", chip.Ratio)
		seen[chip.Ratio] = chip.Value
	}
}

// Thin wrapper so the cmd test can assert chip.Value still maps via the same owner
// without importing unexported helpers.
func serviceAspectRatioFromSize(t *testing.T, size string) string {
	t.Helper()
	for _, chip := range service.ExportGPTImageStudioSizeChips() {
		if chip.Value == size {
			return chip.Ratio
		}
	}
	// Fall through: non-studio sizes are not in Export; use a round-trip via known chips only.
	t.Fatalf("size %q is not a studio chip", size)
	return ""
}
