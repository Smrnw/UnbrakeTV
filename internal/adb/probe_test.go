package adb

import (
	"context"
	"net"
	"testing"
	"time"
)

// A VPN/proxy that accepts every TCP connection must not look like a TV.
func TestProbeRejectsPlainOpenPort(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { time.Sleep(3 * time.Second); c.Close() }()
		}
	}()
	if Probe(context.Background(), ln.Addr().String(), 300*time.Millisecond) {
		t.Fatal("open port without ADB was accepted")
	}
}
