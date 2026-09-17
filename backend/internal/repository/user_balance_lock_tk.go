package repository

import (
	"fmt"
	"sync"
)

// userBalanceLocks serializes same-user balance mutations across repository
// owners in one process (user_repo + usage billing hold) so concurrent paths
// queue instead of stacking PG row-lock waits.
var userBalanceLocks sync.Map // int64 userID -> *sync.Mutex

func withUserBalanceLock(userID int64, fn func() error) error {
	v, _ := userBalanceLocks.LoadOrStore(userID, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok || mu == nil {
		return fmt.Errorf("user balance lock type corruption for user %d", userID)
	}
	mu.Lock()
	defer mu.Unlock()
	return fn()
}
