package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
)

// installTKDrainControls honours the one-shot start-draining marker and wires
// SIGUSR2 to release drain. SIGUSR1 (enter drain) stays in main.go unchanged.
// Contract: docs/approved/design-cn-prod-rolling-deploy.md §2.
func installTKDrainControls() {
	if middleware.ApplyStartDraining(nil) {
		log.Printf("start-draining marker %s present; /health -> 503 until SIGUSR2", middleware.StartDrainingMarkerPath())
	}
	releaseCh := make(chan os.Signal, 1)
	signal.Notify(releaseCh, syscall.SIGUSR2)
	go func() {
		for range releaseCh {
			changed, err := middleware.ReleaseDrain()
			if err != nil {
				log.Printf("SIGUSR2: drain released but marker not removed: %v", err)
			}
			if changed {
				log.Println("SIGUSR2 received; drain released (/health -> 200)")
			} else {
				log.Println("SIGUSR2 received; not draining")
			}
		}
	}()
}
