//go:build unit

package archive

import "testing"

func TestMutableShardStatesIncludesPendingAndFailed(t *testing.T) {
	states := MutableShardStates()
	if len(states) != 2 || states[0] != StatePending || states[1] != StateFailed {
		t.Fatalf("MutableShardStates()=%v", states)
	}
}
