package adb

import (
	"context"
	"testing"
	"time"
)

// Discover must return on time whatever the network looks like
// (no interfaces, VPN adapters, nobody answering).
func TestDiscoverReturnsOnTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	found := Discover(ctx)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Discover took %v", d)
	}
	t.Logf("wireless-debugging devices in this network: %d", len(found))
}
