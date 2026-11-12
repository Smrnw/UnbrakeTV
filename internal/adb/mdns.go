package adb

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Wireless debugging announces itself in the local network over mDNS.
const (
	svcConnect = "_adb-tls-connect._tcp.local"
	svcPairing = "_adb-tls-pairing._tcp.local"
)

// Service is a wireless-debugging endpoint found over mDNS.
type Service struct {
	Pairing bool   `json:"pairing"` // true: port for pairing by code, false: port for connecting
	Address string `json:"address"` // ip:port
	Name    string `json:"name"`
}

func dnsName(b []byte, name string) []byte {
	for _, l := range strings.Split(name, ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

func mdnsQuery() []byte {
	b := []byte{0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0}
	for _, s := range []string{svcConnect, svcPairing} {
		b = dnsName(b, s)
		// PTR, class IN with the "unicast response" bit.
		b = append(b, 0, 12, 0x80, 1)
	}
	return b
}

// readName decodes a possibly compressed DNS name at off.
func readName(msg []byte, off int) (string, int, error) {
	var parts []string
	end, jumps := -1, 0
	for {
		if off >= len(msg) {
			return "", 0, errors.New("dns: truncated")
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.Join(parts, "."), end, nil
		case l&0xc0 == 0xc0:
			if off+1 >= len(msg) || jumps > 16 {
				return "", 0, errors.New("dns: bad pointer")
			}
			if end < 0 {
				end = off + 2
			}
			off = int(l&0x3f)<<8 | int(msg[off+1])
			jumps++
		default:
			if off+1+l > len(msg) {
				return "", 0, errors.New("dns: truncated")
			}
			parts = append(parts, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

// parseMDNS extracts wireless-debugging services from one mDNS response.
// from is the sender's IP, used when the response carries no A record.
func parseMDNS(msg []byte, from string) []Service {
	if len(msg) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(msg[4:]))
	rr := int(binary.BigEndian.Uint16(msg[6:])) + int(binary.BigEndian.Uint16(msg[8:])) + int(binary.BigEndian.Uint16(msg[10:]))
	off := 12
	for i := 0; i < qd; i++ {
		_, n, err := readName(msg, off)
		if err != nil {
			return nil
		}
		off = n + 4
	}
	type srv struct {
		port   int
		target string
	}
	srvs := map[string]srv{}
	hosts := map[string]string{}
	for i := 0; i < rr; i++ {
		name, n, err := readName(msg, off)
		if err != nil || n+10 > len(msg) {
			break
		}
		typ := binary.BigEndian.Uint16(msg[n:])
		dl := int(binary.BigEndian.Uint16(msg[n+8:]))
		data := n + 10
		if data+dl > len(msg) {
			break
		}
		off = data + dl
		switch typ {
		case 33: // SRV
			if dl < 7 {
				continue
			}
			target, _, err := readName(msg, data+6)
			if err != nil {
				continue
			}
			srvs[name] = srv{int(binary.BigEndian.Uint16(msg[data+4:])), strings.ToLower(target)}
		case 1: // A
			if dl == 4 {
				hosts[strings.ToLower(name)] = net.IP(msg[data : data+4]).String()
			}
		}
	}
	var out []Service
	for name, s := range srvs {
		low := strings.ToLower(name)
		pairing := strings.HasSuffix(low, "."+svcPairing)
		if !pairing && !strings.HasSuffix(low, "."+svcConnect) {
			continue
		}
		ip := hosts[s.target]
		if ip == "" {
			ip = from
		}
		inst := name
		if i := strings.Index(low, "._adb-tls-"); i > 0 {
			inst = name[:i]
		}
		out = append(out, Service{Pairing: pairing, Address: net.JoinHostPort(ip, strconv.Itoa(s.port)), Name: inst})
	}
	return out
}

// Discover looks for TVs with wireless debugging enabled. It sends one mDNS
// query from every local IPv4 address and collects answers until ctx is done.
func Discover(ctx context.Context) []Service {
	var (
		mu   sync.Mutex
		seen = map[string]bool{}
		out  []Service
		wg   sync.WaitGroup
	)
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipn.IP})
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			c.SetDeadline(deadline)
			dst := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
			if _, err := c.WriteToUDP(mdnsQuery(), dst); err != nil {
				return
			}
			buf := make([]byte, 9000)
			for {
				n, from, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				for _, s := range parseMDNS(buf[:n], from.IP.String()) {
					mu.Lock()
					if !seen[s.Address] {
						seen[s.Address] = true
						out = append(out, s)
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}
