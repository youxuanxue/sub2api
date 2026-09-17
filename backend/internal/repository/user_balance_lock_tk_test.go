package repository

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUserRepositoryWithUserBalanceLockSerializesSameUser(t *testing.T) {
	t.Parallel()
	repo := &userRepository{}
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = repo.withUserBalanceLock(42, func() error {
				cur := concurrent.Add(1)
				for {
					prev := maxConcurrent.Load()
					if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				concurrent.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	if maxConcurrent.Load() != 1 {
		t.Fatalf("same-user lock must be exclusive, maxConcurrent=%d", maxConcurrent.Load())
	}
}

func TestUserRepositoryWithUserBalanceLockAllowsDifferentUsers(t *testing.T) {
	t.Parallel()
	repo := &userRepository{}
	started := make(chan struct{}, 2)
	release := make(chan struct{})

	var wg sync.WaitGroup
	for _, userID := range []int64{1, 2} {
		wg.Add(1)
		id := userID
		go func() {
			defer wg.Done()
			_ = repo.withUserBalanceLock(id, func() error {
				started <- struct{}{}
				<-release
				return nil
			})
		}()
	}

	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-timeout:
			t.Fatal("different users should acquire locks concurrently")
		}
	}
	close(release)
	wg.Wait()
}
