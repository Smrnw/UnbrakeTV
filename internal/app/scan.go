package app

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/adb"
)

// Found is a device in the local network with the ADB port open.
type Found struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

// Scan probes TCP 5555 on every host of the local /24 networks.
func Scan(ctx context.Context) []Found {
	var targets []net.IP
	seen := map[string]bool{}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			base := net.IPv4(ip[0], ip[1], ip[2], 0).To4()
			if seen[base.String()] {
				continue
			}
			seen[base.String()] = true
			for i := 1; i < 255; i++ {
				t := net.IPv4(base[0], base[1], base[2], byte(i)).To4()
				if !t.Equal(ip) {
					targets = append(targets, t)
				}
			}
		}
	}

	var mu sync.Mutex
	var found []Found
	sem := make(chan struct{}, 128)
	var wg sync.WaitGroup
	d := net.Dialer{Timeout: 700 * time.Millisecond}
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(ip net.IP) {
			defer wg.Done()
			defer func() { <-sem }()
			addr := net.JoinHostPort(ip.String(), "5555")
			if !adb.Probe(ctx, addr, d.Timeout) {
				return
			}
			name := "Android-устройство (ADB отвечает)"
			if n, err := net.DefaultResolver.LookupAddr(ctx, ip.String()); err == nil && len(n) > 0 {
				name = strings.TrimSuffix(n[0], ".")
			}
			mu.Lock()
			found = append(found, Found{Address: addr, Name: name})
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].Address < found[j].Address })
	return found
}
