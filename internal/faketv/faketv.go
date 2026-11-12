// Package faketv emulates an Android TV on the ADB wire-protocol level.
// It powers the demo mode of the program and the test bench (cmd/fakeadb):
// real RSA authentication, wireless debugging with pairing by code,
// shell commands answered with data from a Xiaomi TV, and file upload.
package faketv

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	mr "math/rand/v2"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/adb"
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
	cSTLS = 0x534c5453
)

// FakeAPKPrefix marks a stand-in APK: "FAKEAPK:<package>[;launcher]\n".
// The demo mode uploads such files instead of downloading real ones.
const FakeAPKPrefix = "FAKEAPK:"

// Options configure the emulated TV.
type Options struct {
	// Wireless emulates "Отладка по Wi-Fi" (Android 11+): the connection is
	// upgraded to TLS and only paired keys are accepted.
	Wireless bool
	// PairCode is the six-digit code of the pairing server.
	PairCode string
	// Logf receives log lines; nil means silent.
	Logf func(format string, args ...any)
}

type proc struct {
	name  string
	cpu   float64
	memKB int
}

// TV is one emulated television.
type TV struct {
	opt  Options
	cert tls.Certificate

	mu        sync.Mutex
	known     []*rsa.PublicKey
	all       []string
	removed   map[string]bool
	disabled  map[string]bool
	stopped   map[string]bool
	third     map[string]bool
	stubborn  map[string]bool // refuse "pm uninstall" to exercise the disable fallback
	launchers []string
	home      string
	settings  map[string]string
	files     map[string][]byte
	freedKB   int
	killedAt  time.Time
	cpuTicks  [4]uint64
	listeners []net.Listener
}

var procs = []proc{
	{"com.mitv.tvhome.atv", 14.2, 312 * 1024}, {"system_server", 9.1, 280 * 1024},
	{"com.miui.tv.analytics", 4.4, 88 * 1024}, {"com.xiaomi.statistic", 3.1, 61 * 1024},
	{"com.google.android.gms", 2.9, 190 * 1024}, {"com.google.android.gms.persistent", 1.2, 120 * 1024},
	{"com.mitv.tvhome.mitvplus:remote", 2.5, 95 * 1024}, {"com.google.android.youtube.tv", 1.8, 210 * 1024},
	{"com.netflix.ninja", 0.6, 70 * 1024}, {"com.mediatek.tv.service", 5.0, 150 * 1024},
	{"ru.mts.mtstv", 0.4, 55 * 1024}, {"com.xm.webcontent", 1.4, 48 * 1024}, {"surfaceflinger", 3.0, 40 * 1024},
}

// New creates an emulated TV with the factory set of packages.
func New(opt Options) (*TV, error) {
	t := &TV{
		opt: opt, removed: map[string]bool{}, disabled: map[string]bool{}, stopped: map[string]bool{},
		third: map[string]bool{}, stubborn: map[string]bool{"com.mediatek.tv.service": true},
		settings: map[string]string{}, files: map[string][]byte{},
		launchers: []string{"com.mitv.tvhome.atv", "com.google.android.tvlauncher"},
		home:      "com.mitv.tvhome.atv",
	}
	for _, l := range strings.Split(packagesTxt, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "package:"); ok {
			t.all = append(t.all, p)
			if strings.HasPrefix(p, "ru.") || p == "com.vk.tv" || p == "gpm.tnt_premier" {
				t.third[p] = true
			}
		}
	}
	sort.Strings(t.all)
	if opt.Wireless {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		if t.cert, err = adb.TLSCertificate(k); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *TV) logf(format string, args ...any) {
	if t.opt.Logf != nil {
		t.opt.Logf(format, args...)
	}
}

// Listen starts the ADB server and returns its address.
func (t *TV) Listen(addr string) (string, error) {
	return t.listen(addr, t.serve)
}

// ListenPairing starts the pairing-by-code server (wireless debugging).
func (t *TV) ListenPairing(addr string) (string, error) {
	if !t.opt.Wireless {
		return "", errors.New("pairing needs Options.Wireless")
	}
	return t.listen(addr, t.servePairing)
}

func (t *TV) listen(addr string, h func(net.Conn)) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	t.listeners = append(t.listeners, ln)
	t.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h(c)
		}
	}()
	return ln.Addr().String(), nil
}

// Close stops all listeners.
func (t *TV) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, l := range t.listeners {
		l.Close()
	}
	t.listeners = nil
}

func send(c io.Writer, cmd, a0, a1 uint32, data []byte) {
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

func recv(c io.Reader) (cmd, a0, a1 uint32, data []byte, err error) {
	h := make([]byte, 24)
	if _, err = io.ReadFull(c, h); err != nil {
		return
	}
	cmd = binary.LittleEndian.Uint32(h[0:])
	a0 = binary.LittleEndian.Uint32(h[4:])
	a1 = binary.LittleEndian.Uint32(h[8:])
	n := binary.LittleEndian.Uint32(h[12:])
	sum := binary.LittleEndian.Uint32(h[16:])
	if n > 1<<20 {
		err = fmt.Errorf("packet too large")
		return
	}
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
	s := string(b)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, " "); i > 0 {
		s = s[:i]
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 {
		return nil, fmt.Errorf("bad key length %d", len(raw))
	}
	words := int(binary.LittleEndian.Uint32(raw))
	if words <= 0 || words > 1024 || len(raw) != 4+4+words*8+4 {
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

func (t *TV) knows(k *rsa.PublicKey) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, x := range t.known {
		if x.N.Cmp(k.N) == 0 && x.E == k.E {
			return true
		}
	}
	return false
}

// servePairing is the device side of pairing by code.
func (t *TV) servePairing(raw net.Conn) {
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	c := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{t.cert}, MinVersion: tls.VersionTLS13, ClientAuth: tls.RequestClientCert})
	if err := c.Handshake(); err != nil {
		return
	}
	st := c.ConnectionState()
	km, err := st.ExportKeyingMaterial(adb.ExportedKeyLabel, nil, 64)
	if err != nil {
		return
	}
	sp, err := adb.NewSpake2(false, append([]byte(t.opt.PairCode), km...))
	if err != nil {
		return
	}
	if adb.WritePairingPacket(c, 0, sp.Msg()) != nil {
		return
	}
	their, err := adb.ReadPairingPacket(c, 0)
	if err != nil {
		return
	}
	secret, err := sp.Finish(their)
	if err != nil {
		return
	}
	ciph, err := adb.NewPairingCipher(secret)
	if err != nil {
		return
	}
	enc, err := adb.ReadPairingPacket(c, 1)
	if err != nil {
		return
	}
	info, err := ciph.Open(enc)
	if err != nil || len(info) != adb.PeerInfoSize || info[0] != 0 {
		t.logf("pairing rejected: wrong code")
		return
	}
	k, err := parseAndroidKey(info[1:])
	if err != nil {
		t.logf("pairing rejected: %v", err)
		return
	}
	t.mu.Lock()
	t.known = append(t.known, k)
	t.mu.Unlock()
	t.logf("paired with a new computer")
	reply := make([]byte, adb.PeerInfoSize)
	reply[0] = 1 // ADB_DEVICE_GUID
	copy(reply[1:], "adb-fake-tv-0001")
	adb.WritePairingPacket(c, 1, ciph.Seal(reply))
}

func (t *TV) serve(raw net.Conn) {
	defer raw.Close()
	var c io.ReadWriter = raw
	if cmd, _, _, _, err := recv(c); err != nil || cmd != cCNXN {
		return
	}
	if t.opt.Wireless {
		send(c, cSTLS, 0x01000000, 0, nil)
		if cmd, _, _, _, err := recv(c); err != nil || cmd != cSTLS {
			return
		}
		tc := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{t.cert}, MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert})
		if err := tc.Handshake(); err != nil {
			return
		}
		certs := tc.ConnectionState().PeerCertificates
		var pub *rsa.PublicKey
		if len(certs) > 0 {
			if c0, err := x509.ParseCertificate(certs[0].Raw); err == nil {
				pub, _ = c0.PublicKey.(*rsa.PublicKey)
			}
		}
		if pub == nil || !t.knows(pub) {
			t.logf("TLS connection rejected: the computer is not paired")
			return
		}
		c = tc
	} else if !t.auth(c) {
		return
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
		if svc == "sync:" {
			if !t.serveSync(c, remote, local) {
				return
			}
			continue
		}
		cmdline, ok := strings.CutPrefix(svc, "shell:")
		if !ok {
			send(c, cCLSE, 0, local, nil)
			continue
		}
		if arg, ok := strings.CutPrefix(cmdline, "sleep "); ok {
			// A slow command, to test timeouts and interruption.
			sec, _ := strconv.Atoi(strings.TrimSpace(arg))
			time.Sleep(time.Duration(min(sec, 60)) * time.Second)
		}
		out := t.shell(cmdline)
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

// auth is the classic RSA handshake of "USB debugging" over the network.
func (t *TV) auth(c io.ReadWriter) bool {
	token := make([]byte, 20)
	rand.Read(token)
	send(c, cAUTH, 1, 0, token)
	for {
		cmd, a0, _, data, err := recv(c)
		if err != nil || cmd != cAUTH {
			return false
		}
		switch a0 {
		case 2: // signature
			ok := false
			t.mu.Lock()
			for _, k := range t.known {
				if rsa.VerifyPKCS1v15(k, crypto.SHA1, token, data) == nil {
					ok = true
				}
			}
			t.mu.Unlock()
			if ok {
				t.logf("signature accepted (known key)")
				return true
			}
			rand.Read(token)
			send(c, cAUTH, 1, 0, token)
		case 3: // public key: "user pressed OK on the TV"
			k, err := parseAndroidKey(data)
			if err != nil {
				t.logf("bad public key: %v", err)
				return false
			}
			t.logf("new key accepted, user confirmed on TV")
			t.mu.Lock()
			t.known = append(t.known, k)
			t.mu.Unlock()
			return true
		}
	}
}

// serveSync implements the receiving half of "adb push".
func (t *TV) serveSync(c io.ReadWriter, remote, local uint32) bool {
	send(c, cOKAY, remote, local, nil)
	var buf, file []byte
	path := ""
	for {
		cmd, _, _, data, err := recv(c)
		if err != nil {
			return false
		}
		if cmd == cCLSE {
			send(c, cCLSE, remote, local, nil)
			return true
		}
		if cmd != cWRTE {
			continue
		}
		send(c, cOKAY, remote, local, nil)
		buf = append(buf, data...)
		for len(buf) >= 8 {
			id, n := string(buf[:4]), int(binary.LittleEndian.Uint32(buf[4:]))
			if id == "SEND" || id == "DATA" {
				if len(buf) < 8+n {
					break
				}
				if id == "SEND" {
					path, _, _ = strings.Cut(string(buf[8:8+n]), ",")
					file = nil
				} else {
					file = append(file, buf[8:8+n]...)
				}
				buf = buf[8+n:]
				continue
			}
			buf = buf[8:]
			switch id {
			case "DONE":
				t.mu.Lock()
				t.files[path] = file
				t.mu.Unlock()
				t.logf("received %s (%d bytes)", path, len(file))
				send(c, cWRTE, remote, local, []byte("OKAY\x00\x00\x00\x00"))
				if cmd, _, _, _, err := recv(c); err != nil || cmd != cOKAY {
					return false
				}
			case "QUIT":
				send(c, cCLSE, remote, local, nil)
				_, _, _, _, err := recv(c) // client CLSE
				return err == nil
			default:
				msg := "unknown sync command"
				hdr := make([]byte, 8)
				copy(hdr, "FAIL")
				binary.LittleEndian.PutUint32(hdr[4:], uint32(len(msg)))
				send(c, cWRTE, remote, local, append(hdr, msg...))
				recv(c)
			}
		}
	}
}

func (t *TV) gone(p string) bool { return t.removed[p] || t.disabled[p] }

func (t *TV) list(filter func(string) bool) string {
	var sb strings.Builder
	for _, p := range t.all {
		if filter(p) {
			sb.WriteString("package:" + p + "\n")
		}
	}
	return sb.String()
}

// running returns the processes that are alive (caller holds t.mu).
func (t *TV) running() []proc {
	var out []proc
	for _, p := range procs {
		base := strings.SplitN(p.name, ":", 2)[0]
		if base == "com.google.android.gms.persistent" {
			base = "com.google.android.gms"
		}
		if t.gone(base) || t.stopped[base] {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (t *TV) topOut() string {
	var sb strings.Builder
	for _, p := range t.running() {
		fmt.Fprintf(&sb, "%5.1f %6s %s\n", p.cpu*(0.7+0.6*mr.Float64()), strconv.Itoa(p.memKB/1024)+"M", p.name)
	}
	return sb.String()
}

// memAvailKB grows when bloat is removed or background apps are killed.
func (t *TV) memAvailKB() int {
	used := 0
	for _, p := range t.running() {
		used += p.memKB
	}
	total := 0
	for _, p := range procs {
		total += p.memKB
	}
	avail := 250000 + (total-used)*8/10 + mr.IntN(30000)
	if time.Since(t.killedAt) < 30*time.Second {
		avail += 140000
	}
	return min(avail, 1400000)
}

func (t *TV) sizeKB(i int) int { return (i%17 + 1) * 3100 }

func (t *TV) usedKB() int {
	used := 4380000 - t.freedKB
	for i, p := range t.all {
		if t.removed[p] {
			used -= t.sizeKB(i)
		}
	}
	return max(used, 900000)
}

func (t *TV) df() string {
	const total = 5160000
	used := t.usedKB()
	return fmt.Sprintf("Filesystem     1K-blocks    Used Available Use%% Mounted on\n/dev/block/dm-5  %d %d %d  %d%% /data\n", total, used, total-used, used*100/total)
}

func (t *TV) homeNow() string {
	if t.home != "" && !t.gone(t.home) {
		return t.home
	}
	for _, l := range t.launchers {
		if !t.gone(l) {
			return l
		}
	}
	return ""
}

func (t *TV) install(path string) string {
	b, ok := t.files[path]
	if !ok {
		return "Failure [INSTALL_FAILED_INVALID_URI]\n"
	}
	pkg, launcher := "com.example.sideloaded", false
	if rest, ok := strings.CutPrefix(string(b[:min(len(b), 200)]), FakeAPKPrefix); ok {
		line, _, _ := strings.Cut(rest, "\n")
		pkg, _, _ = strings.Cut(line, ";")
		launcher = strings.Contains(line, ";launcher")
	} else if len(b) < 4 || string(b[:2]) != "PK" {
		return "Failure [INSTALL_PARSE_FAILED_NOT_APK: Failed to parse " + path + "]\n"
	}
	known := false
	for _, p := range t.all {
		known = known || p == pkg
	}
	if !known {
		t.all = append(t.all, pkg)
		sort.Strings(t.all)
	}
	t.third[pkg] = true
	delete(t.removed, pkg)
	delete(t.disabled, pkg)
	if launcher {
		has := false
		for _, l := range t.launchers {
			has = has || l == pkg
		}
		if !has {
			t.launchers = append(t.launchers, pkg)
		}
	}
	t.logf("installed %s", pkg)
	return "Success\n"
}

func (t *TV) shell(cmd string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := strings.Fields(cmd)
	last := ""
	if len(f) > 0 {
		last = f[len(f)-1]
	}
	// "pm list packages --user 0 -d" → "pm list packages -d"
	plain := strings.Replace(cmd, " --user 0", "", 1)
	switch {
	case strings.HasPrefix(cmd, "sleep "):
		return ""
	case strings.HasPrefix(cmd, "getprop"):
		return "Xiaomi\nMi TV A2 43\n11\n30\narmeabi-v7a\n2\n"
	case plain == "pm list packages -u":
		return t.list(func(string) bool { return true })
	case plain == "pm list packages":
		return t.list(func(p string) bool { return !t.removed[p] })
	case plain == "pm list packages -d":
		return t.list(func(p string) bool { return t.disabled[p] && !t.removed[p] })
	case plain == "pm list packages -3":
		return t.list(func(p string) bool { return t.third[p] && !t.removed[p] })
	case strings.Contains(cmd, "query-activities"):
		s := "com.android.tv.settings/.FallbackHome\n"
		for _, l := range t.launchers {
			if !t.gone(l) {
				s += l + "/.MainActivity\n"
			}
		}
		return s
	case strings.Contains(cmd, "resolve-activity"):
		h := t.homeNow()
		if h == "" {
			h = "com.android.tv.settings"
		}
		return "priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\n" + h + "/.MainActivity\n"
	case strings.Contains(cmd, "set-home-activity"):
		pkg := ""
		for _, x := range f {
			if i := strings.Index(x, "/"); i > 0 {
				pkg = x[:i]
			}
		}
		for _, l := range t.launchers {
			if l == pkg && !t.gone(l) {
				t.home = pkg
				return "Success\n"
			}
		}
		return "Error: Activity not found: " + last + "\n"
	case strings.HasPrefix(cmd, "ime list"):
		return "com.google.android.inputmethod.latin/com.android.inputmethod.latin.LatinIME\n"
	case strings.HasPrefix(cmd, "dumpsys diskstats"):
		var names, sizes []string
		for i, p := range t.all {
			if t.removed[p] {
				continue
			}
			names = append(names, `"`+p+`"`)
			sizes = append(sizes, fmt.Sprint(t.sizeKB(i)*1000))
		}
		return "Latency: 1ms [512B Data Write]\nPackage Names: [" + strings.Join(names, ",") + "]\nApp Sizes: [" + strings.Join(sizes, ",") + "]\n"
	case strings.HasPrefix(cmd, "head -1 /proc/stat"):
		t.cpuTicks[0] += uint64(4 * len(t.running()))
		t.cpuTicks[2] += uint64(10 + mr.IntN(20))
		t.cpuTicks[3] += uint64(100 + mr.IntN(50))
		k := t.cpuTicks
		return fmt.Sprintf("cpu  %d 0 %d %d 0 0 0 0 0 0\n@@@\nMemTotal:        1530000 kB\nMemFree:          80000 kB\nMemAvailable:    %d kB\n@@@\n%s@@@\n58000\n46500\n@@@\n%s",
			k[0], k[2], k[3], t.memAvailKB(), t.df(), t.topOut())
	case strings.HasPrefix(cmd, "top"):
		return t.topOut()
	case strings.HasPrefix(cmd, "ps "):
		return ""
	case strings.Contains(cmd, "MemAvailable"):
		return fmt.Sprintf("MemAvailable:    %d kB\n", t.memAvailKB())
	case strings.HasPrefix(cmd, "settings get global"):
		if v, ok := t.settings[last]; ok {
			return v + "\n"
		}
		return "null\n"
	case strings.HasPrefix(cmd, "settings put global"):
		for _, part := range strings.Split(cmd, ";") {
			ff := strings.Fields(part)
			if len(ff) == 5 {
				t.settings[ff[3]] = ff[4]
			}
		}
		return ""
	case strings.HasPrefix(cmd, "pm trim-caches"):
		t.freedKB = min(t.freedKB+350000, 700000)
		return ""
	case strings.HasPrefix(cmd, "df"):
		return t.df()
	case cmd == "reboot":
		t.stopped = map[string]bool{}
		t.killedAt = time.Time{}
		return ""
	case strings.HasPrefix(cmd, "am kill-all"):
		t.killedAt = time.Now()
		return ""
	case strings.HasPrefix(cmd, "am force-stop"):
		t.stopped[last] = true
		return ""
	case strings.HasPrefix(cmd, "am start"):
		return "Starting: Intent { act=android.intent.action.MAIN }\n"
	case strings.HasPrefix(cmd, "rm "):
		delete(t.files, last)
		return ""
	case strings.HasPrefix(cmd, "pm install "):
		for _, x := range f {
			if strings.HasPrefix(x, "/") {
				return t.install(x)
			}
		}
		return "Error: no APK path\n"
	case strings.HasPrefix(cmd, "pm uninstall"):
		if t.stubborn[last] {
			return "Failure [DELETE_FAILED_INTERNAL_ERROR]\n"
		}
		t.removed[last] = true
		return "Success\n"
	case strings.HasPrefix(cmd, "pm disable-user"):
		t.disabled[last] = true
		return "Package " + last + " new state: disabled-user\n"
	case strings.Contains(cmd, "install-existing"):
		p := last
		if i := strings.Index(cmd, "||"); i > 0 {
			ff := strings.Fields(cmd[:i])
			p = ff[len(ff)-1]
			if p == "2>&1" && len(ff) > 1 {
				p = ff[len(ff)-2]
			}
		}
		if t.third[p] && t.removed[p] {
			return "android.content.pm.PackageManager$NameNotFoundException: Package " + p + " doesn't exist\n"
		}
		delete(t.removed, p)
		return "Package " + p + " installed for user: 0\n"
	case strings.HasPrefix(cmd, "pm enable"):
		p := last
		if p == "2>&1" && len(f) > 1 {
			p = f[len(f)-2]
		}
		if t.removed[p] {
			return "Error: java.lang.IllegalArgumentException: Unknown package: " + p + "\n"
		}
		delete(t.disabled, p)
		return "Package " + p + " new state: enabled\n"
	}
	return "/system/bin/sh: " + cmd + ": not found\n"
}

// StdLogf adapts the standard logger to Options.Logf.
func StdLogf(format string, args ...any) { log.Printf(format, args...) }
