// fakeadb is a tiny ADB device emulator for developing Unbrake TV without a TV.
// It speaks the real ADB protocol (including RSA authentication) and answers
// shell commands with data from a Xiaomi TV.
//
//	go run ./cmd/fakeadb -listen 127.0.0.1:5555
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	mr "math/rand/v2"
	"net"
	"sort"
	"strings"
	"sync"
)

//go:embed testdata/packages.txt
var packagesTxt string

const (
	cCNXN = 0x4e584e43
	cAUTH = 0x48545541
	cOPEN = 0x4e45504f
	cOKAY = 0x59414b4f
	cCLSE = 0x45534c43
	cWRTE = 0x45545257
)

var (
	mu       sync.Mutex
	known    []*rsa.PublicKey
	all      []string
	removed  = map[string]bool{}
	disabled = map[string]bool{}
	third    = map[string]bool{}
	// Packages that refuse "pm uninstall" to exercise the disable fallback.
	stubborn = map[string]bool{"com.mediatek.tv.service": true}
	settings = map[string]string{}
	freedKB  int
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5555", "address")
	flag.Parse()
	for _, l := range strings.Split(packagesTxt, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "package:"); ok {
			all = append(all, p)
			if strings.HasPrefix(p, "ru.") || p == "com.vk.tv" || p == "gpm.tnt_premier" {
				third[p] = true
			}
		}
	}
	sort.Strings(all)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("fake TV listening on", *listen)
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go serve(c)
	}
}

func send(c net.Conn, cmd, a0, a1 uint32, data []byte) {
	h := make([]byte, 24)
	binary.LittleEndian.PutUint32(h[0:], cmd)
	binary.LittleEndian.PutUint32(h[4:], a0)
	binary.LittleEndian.PutUint32(h[8:], a1)
	binary.LittleEndian.PutUint32(h[12:], uint32(len(data)))
	var s uint32
	for _, b := range data {
		s += uint32(b)
	}
	binary.LittleEndian.PutUint32(h[16:], s)
	binary.LittleEndian.PutUint32(h[20:], cmd^0xffffffff)
	c.Write(append(h, data...))
}

func recv(c net.Conn) (cmd, a0, a1 uint32, data []byte, err error) {
	h := make([]byte, 24)
	if _, err = io.ReadFull(c, h); err != nil {
		return
	}
	cmd = binary.LittleEndian.Uint32(h[0:])
	a0 = binary.LittleEndian.Uint32(h[4:])
	a1 = binary.LittleEndian.Uint32(h[8:])
	n := binary.LittleEndian.Uint32(h[12:])
	sum := binary.LittleEndian.Uint32(h[16:])
	data = make([]byte, n)
	_, err = io.ReadFull(c, data)
	var s uint32
	for _, b := range data {
		s += uint32(b)
	}
	if err == nil && s != sum {
		err = fmt.Errorf("bad checksum")
	}
	return
}

// parseAndroidKey decodes the mincrypt RSAPublicKey format back into an rsa.PublicKey.
func parseAndroidKey(b []byte) (*rsa.PublicKey, error) {
	s := strings.TrimRight(string(b), "\x00")
	if i := strings.Index(s, " "); i > 0 {
		s = s[:i]
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	words := int(binary.LittleEndian.Uint32(raw))
	if len(raw) != 4+4+words*8+4 {
		return nil, fmt.Errorf("bad key length %d", len(raw))
	}
	n := new(big.Int)
	for i := words - 1; i >= 0; i-- {
		n.Lsh(n, 32)
		n.Or(n, big.NewInt(int64(binary.LittleEndian.Uint32(raw[8+4*i:]))))
	}
	n0inv := binary.LittleEndian.Uint32(raw[4:])
	if uint32(n.Uint64())*n0inv != 0xffffffff { // n0 * (-1/n0) == -1 mod 2^32
		return nil, fmt.Errorf("bad n0inv")
	}
	rr := new(big.Int)
	for i := words - 1; i >= 0; i-- {
		rr.Lsh(rr, 32)
		rr.Or(rr, big.NewInt(int64(binary.LittleEndian.Uint32(raw[8+words*4+4*i:]))))
	}
	want := new(big.Int).Exp(big.NewInt(2), big.NewInt(int64(words*32*2)), n)
	if rr.Cmp(want) != 0 {
		return nil, fmt.Errorf("bad rr")
	}
	e := int(binary.LittleEndian.Uint32(raw[8+words*8:]))
	return &rsa.PublicKey{N: n, E: e}, nil
}

func serve(c net.Conn) {
	defer c.Close()
	if cmd, _, _, _, err := recv(c); err != nil || cmd != cCNXN {
		return
	}
	token := make([]byte, 20)
	rand.Read(token)
	send(c, cAUTH, 1, 0, token)
	authed := false
	for !authed {
		cmd, a0, _, data, err := recv(c)
		if err != nil || cmd != cAUTH {
			return
		}
		switch a0 {
		case 2: // signature
			mu.Lock()
			for _, k := range known {
				if rsa.VerifyPKCS1v15(k, crypto.SHA1, token, data) == nil {
					authed = true
				}
			}
			mu.Unlock()
			if !authed {
				rand.Read(token)
				send(c, cAUTH, 1, 0, token)
			} else {
				log.Println("signature accepted (known key)")
			}
		case 3: // public key: "user pressed OK on the TV"
			k, err := parseAndroidKey(data)
			if err != nil {
				log.Println("bad public key:", err)
				return
			}
			log.Println("new key accepted, user confirmed on TV")
			mu.Lock()
			known = append(known, k)
			mu.Unlock()
			authed = true
		}
	}
	send(c, cCNXN, 0x01000000, 256*1024, []byte("device::ro.product.name=fake;ro.product.model=MiTV;\x00"))
	var remote uint32 = 1000
	for {
		cmd, a0, _, data, err := recv(c)
		if err != nil {
			return
		}
		if cmd != cOPEN {
			continue
		}
		remote++
		local := a0
		svc := strings.TrimRight(string(data), "\x00")
		out := shell(strings.TrimPrefix(svc, "shell:"))
		send(c, cOKAY, remote, local, nil)
		b := []byte(out)
		for len(b) > 0 {
			n := min(len(b), 4096)
			send(c, cWRTE, remote, local, b[:n])
			b = b[n:]
			if cmd, _, _, _, err := recv(c); err != nil || cmd != cOKAY {
				return
			}
		}
		send(c, cCLSE, remote, local, nil)
		recv(c) // client CLSE
	}
}

func list(filter func(string) bool) string {
	var sb strings.Builder
	for _, p := range all {
		if filter(p) {
			sb.WriteString("package:" + p + "\n")
		}
	}
	return sb.String()
}

func topOut() string {
	procs := []struct {
		n   string
		cpu float64
		mem string
	}{
		{"com.mitv.tvhome.atv", 14.2, "312M"}, {"system_server", 9.1, "280M"},
		{"com.miui.tv.analytics", 4.4, "88M"}, {"com.xiaomi.statistic", 3.1, "61M"},
		{"com.google.android.gms", 2.9, "190M"}, {"com.google.android.gms.persistent", 1.2, "120M"},
		{"com.mitv.tvhome.mitvplus:remote", 2.5, "95M"}, {"com.google.android.youtube.tv", 1.8, "210M"},
		{"com.netflix.ninja", 0.6, "70M"}, {"com.mediatek.tv.service", 5.0, "150M"},
		{"ru.mts.mtstv", 0.4, "55M"}, {"com.xm.webcontent", 1.4, "48M"}, {"surfaceflinger", 3.0, "40M"},
	}
	var sb strings.Builder
	mu.Lock()
	defer mu.Unlock()
	for _, p := range procs {
		base := strings.SplitN(p.n, ":", 2)[0]
		if removed[base] || disabled[base] {
			continue
		}
		fmt.Fprintf(&sb, "%5.1f %6s %s\n", p.cpu*(0.7+0.6*mrand()), p.mem, p.n)
	}
	return sb.String()
}

func mrand() float64 { return mr.Float64() }

var cpuTicks [4]uint64

func shell(cmd string) string {
	f := strings.Fields(cmd)
	last := ""
	if len(f) > 0 {
		last = f[len(f)-1]
	}
	switch {
	case strings.HasPrefix(cmd, "getprop"):
		return "Xiaomi\nMi TV A2 43\n11\n30\n2\n"
	case cmd == "pm list packages -u":
		return list(func(string) bool { return true })
	case cmd == "pm list packages":
		mu.Lock()
		defer mu.Unlock()
		return list(func(p string) bool { return !removed[p] })
	case cmd == "pm list packages -d":
		mu.Lock()
		defer mu.Unlock()
		return list(func(p string) bool { return disabled[p] && !removed[p] })
	case cmd == "pm list packages -3":
		mu.Lock()
		defer mu.Unlock()
		return list(func(p string) bool { return third[p] && !removed[p] })
	case strings.Contains(cmd, "query-activities"):
		mu.Lock()
		defer mu.Unlock()
		s := "com.android.tv.settings/.FallbackHome\n"
		for _, l := range []string{"com.mitv.tvhome.atv", "com.google.android.tvlauncher"} {
			if !removed[l] {
				s += l + "/.MainActivity\n"
			}
		}
		return s
	case strings.Contains(cmd, "resolve-activity"):
		mu.Lock()
		defer mu.Unlock()
		if !removed["com.mitv.tvhome.atv"] {
			return "priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\ncom.mitv.tvhome.atv/.MainActivity\n"
		}
		return "priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\ncom.google.android.tvlauncher/.MainActivity\n"
	case strings.HasPrefix(cmd, "ime list"):
		return "com.google.android.inputmethod.latin/com.android.inputmethod.latin.LatinIME\n"
	case strings.HasPrefix(cmd, "dumpsys diskstats"):
		var names, sizes []string
		for i, p := range all {
			names = append(names, `"`+p+`"`)
			sizes = append(sizes, fmt.Sprint((i%17+1)*3_100_000))
		}
		return "Latency: 1ms [512B Data Write]\nPackage Names: [" + strings.Join(names, ",") + "]\nApp Sizes: [" + strings.Join(sizes, ",") + "]\n"
	case strings.HasPrefix(cmd, "head -1 /proc/stat"):
		mu.Lock()
		cpuTicks[0] += uint64(30 + mr.IntN(40))
		cpuTicks[2] += uint64(10 + mr.IntN(20))
		cpuTicks[3] += uint64(100 + mr.IntN(50))
		t := cpuTicks
		mu.Unlock()
		return fmt.Sprintf("cpu  %d 0 %d %d 0 0 0 0 0 0\n@@@\nMemTotal:        1530000 kB\nMemFree:          80000 kB\nMemAvailable:    %d kB\n@@@\nFilesystem     1K-blocks    Used Available Use%% Mounted on\n/dev/block/dm-5  5160000 4380000   780000  85%% /data\n@@@\n58000\n@@@\n%s",
			t[0], t[2], t[3], 280000+mr.IntN(40000), topOut())
	case strings.HasPrefix(cmd, "top"):
		return topOut()
	case strings.HasPrefix(cmd, "settings get global"):
		mu.Lock()
		defer mu.Unlock()
		if v, ok := settings[last]; ok {
			return v + "\n"
		}
		return "null\n"
	case strings.HasPrefix(cmd, "settings put global"):
		mu.Lock()
		defer mu.Unlock()
		for _, part := range strings.Split(cmd, ";") {
			ff := strings.Fields(part)
			if len(ff) == 5 {
				settings[ff[3]] = ff[4]
			}
		}
		return ""
	case strings.HasPrefix(cmd, "pm trim-caches"):
		mu.Lock()
		freedKB += 350000
		mu.Unlock()
		return ""
	case cmd == "df -k /data":
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("Filesystem     1K-blocks    Used Available Use%% Mounted on\n/dev/block/dm-5  5160000 %d %d  80%% /data\n", 4380000-freedKB, 780000+freedKB)
	case cmd == "reboot":
		return ""
	case strings.HasPrefix(cmd, "pm uninstall"):
		if stubborn[last] {
			return "Failure [DELETE_FAILED_INTERNAL_ERROR]\n"
		}
		mu.Lock()
		removed[last] = true
		mu.Unlock()
		return "Success\n"
	case strings.HasPrefix(cmd, "pm disable-user"):
		mu.Lock()
		disabled[last] = true
		mu.Unlock()
		return "Package " + last + " new state: disabled-user\n"
	case strings.Contains(cmd, "install-existing"):
		p := f[len(f)-1]
		if i := strings.Index(cmd, "||"); i > 0 {
			ff := strings.Fields(cmd[:i])
			p = ff[len(ff)-2]
			if p == "2>&1" {
				p = ff[len(ff)-3]
			}
		}
		mu.Lock()
		delete(removed, p)
		mu.Unlock()
		return "Package " + p + " installed for user: 0\n"
	case strings.HasPrefix(cmd, "pm enable"):
		p := f[len(f)-2]
		mu.Lock()
		delete(disabled, p)
		mu.Unlock()
		return "Package " + p + " new state: enabled\n"
	}
	return "/system/bin/sh: " + cmd + ": not found\n"
}
