//go:build integration

package repository

import "testing"

func TestIntegrationRegistryOnlyRequestGuard(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		arguments []string
		want      bool
		wantError bool
	}{
		{
			name:      "disabled ignores list flag",
			arguments: []string{"-test.list", "TestOne"},
		},
		{
			name:      "enabled accepts split list flag",
			enabled:   true,
			arguments: []string{"-test.list", "TestOne"},
			want:      true,
		},
		{
			name:      "enabled accepts assigned list flag",
			enabled:   true,
			arguments: []string{"-test.list=TestOne"},
			want:      true,
		},
		{
			name:      "enabled rejects ordinary test execution",
			enabled:   true,
			arguments: []string{"-test.run", "TestOne"},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := integrationRegistryOnlyRequested(test.enabled, test.arguments)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %v", err, test.wantError)
			}
			if got != test.want {
				t.Fatalf("requested = %v, want %v", got, test.want)
			}
		})
	}
}
