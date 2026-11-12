package adb

import (
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"
)

func bigOne() *big.Int           { return big.NewInt(1) }
func hexOf(b []byte) string      { return hex.EncodeToString(b) }
func u16(b []byte, v int) []byte { return binary.BigEndian.AppendUint16(b, uint16(v)) }

// rr appends one resource record.
func rr(b []byte, name string, typ int, data []byte) []byte {
	b = dnsName(b, name)
	b = u16(b, typ)
	b = u16(b, 1)
	b = append(b, 0, 0, 0, 120)
	b = u16(b, len(data))
	return append(b, data...)
}

func TestParseMDNS(t *testing.T) {
	const inst = "adb-R58M1234-AbCdEf._adb-tls-connect._tcp.local"
	const pair = "adb-R58M1234-AbCdEf._adb-tls-pairing._tcp.local"
	msg := []byte{0, 0, 0x84, 0, 0, 0, 0, 2, 0, 0, 0, 3}
	msg = rr(msg, svcConnect, 12, dnsName(nil, inst))
	msg = rr(msg, svcPairing, 12, dnsName(nil, pair))
	// SRV: priority, weight, port, target — the target uses a compression pointer.
	hostOff := len(msg)
	msg = rr(msg, "Android.local", 1, []byte{192, 168, 1, 77})
	ptr := []byte{0xc0, byte(hostOff)}
	msg = rr(msg, inst, 33, append([]byte{0, 0, 0, 0, 0xa1, 0x13}, ptr...))
	msg = rr(msg, pair, 33, append([]byte{0, 0, 0, 0, 0x91, 0x03}, ptr...))

	got := parseMDNS(msg, "10.0.0.9")
	if len(got) != 2 {
		t.Fatalf("got %d services: %+v", len(got), got)
	}
	want := map[string]bool{"192.168.1.77:41235": false, "192.168.1.77:37123": true}
	for _, s := range got {
		p, ok := want[s.Address]
		if !ok || p != s.Pairing || s.Name != "adb-R58M1234-AbCdEf" {
			t.Errorf("unexpected service %+v", s)
		}
	}

	// Without an A record the sender's address is used.
	msg2 := []byte{0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0}
	msg2 = rr(msg2, inst, 33, append([]byte{0, 0, 0, 0, 0x15, 0xb3}, dnsName(nil, "tv.local")...))
	if got := parseMDNS(msg2, "10.0.0.9"); len(got) != 1 || got[0].Address != "10.0.0.9:5555" {
		t.Errorf("fallback address: %+v", got)
	}

	// Garbage and pointer loops must not crash or hang.
	for _, bad := range [][]byte{nil, {1, 2, 3}, append([]byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}, 0xc0, 12), msg[:len(msg)-5]} {
		parseMDNS(bad, "1.1.1.1")
	}
	if q := mdnsQuery(); len(q) < 12 || q[5] != 2 {
		t.Error("query must carry two questions")
	}
}
