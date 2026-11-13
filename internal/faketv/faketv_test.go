package faketv

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/adb"
	"github.com/Smrnw/UnbrakeTV/internal/tv"
)

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func start(t *testing.T, opt Options) (*TV, string) {
	t.Helper()
	f, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := f.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f, addr
}

// Classic "USB debugging over the network": the full client stack against the emulator.
func TestClassicSession(t *testing.T) {
	_, addr := start(t, Options{})
	k := key(t)
	prompted := false
	c, err := adb.Dial(addr, k, 5*time.Second, func() { prompted = true })
	if err != nil {
		t.Fatal(err)
	}
	if !prompted || c.TLS {
		t.Fatal("first connection must go through the confirmation dialog, without TLS")
	}
	if !adb.Probe(context.Background(), addr, time.Second) {
		t.Fatal("Probe must recognise the TV")
	}
	d := tv.New(c)
	info, err := d.Info()
	if err != nil || info.Manufacturer != "Xiaomi" || info.SDK != 30 || info.ABI != "armeabi-v7a" || !info.IsTV {
		t.Fatalf("info: %+v %v", info, err)
	}
	pk, err := d.Packages()
	if err != nil || len(pk) < 100 {
		t.Fatalf("packages: %d %v", len(pk), err)
	}
	byID := map[string]tv.Package{}
	for _, p := range pk {
		byID[p.ID] = p
	}
	if p := byID["com.mitv.tvhome.atv"]; !p.IsDefaultLauncher || !p.Running || p.MemKB == 0 || p.SizeBytes == 0 {
		t.Errorf("PatchWall: %+v", p)
	}
	if p := byID["com.google.android.inputmethod.latin"]; !p.IsKeyboard || p.Level != tv.Critical {
		t.Errorf("the only keyboard must be critical: %+v", p)
	}
	if p := byID["com.google.android.gms"]; p.MemKB < 300*1024 {
		t.Errorf("gms.persistent must be folded into gms: %d", p.MemKB)
	}

	// Stats: two polls give a CPU figure; processes are folded and sorted.
	d.Stats()
	st, err := d.Stats()
	if err != nil || st.CPU <= 0 || st.MemTotalKB == 0 || st.StorTotalKB == 0 || st.TempC != 58 || len(st.Top) == 0 {
		t.Fatalf("stats: %+v %v", st, err)
	}
	for _, p := range st.Top {
		if p.Package == "com.google.android.gms.persistent" {
			t.Error("process was not folded into its package")
		}
	}

	// Remove → the stubborn package falls back to "disable"; restore brings both back.
	res := d.Remove([]string{"com.miui.tv.analytics", "com.mediatek.tv.service", "bad name;rm"})
	if !res[0].OK || res[0].Method != "uninstall" || !res[1].OK || res[1].Method != "disable" || res[2].OK {
		t.Fatalf("remove: %+v", res)
	}
	res = d.Restore([]string{"com.miui.tv.analytics", "com.mediatek.tv.service", "com.not.installed"})
	if !res[0].OK || !res[1].OK || res[2].OK {
		t.Fatalf("restore: %+v", res)
	}

	// Tweaks.
	if _, err := d.Tweak("anim_fast"); err != nil || d.Tweaks().AnimScale != "0.5" {
		t.Fatal("animation scale was not applied")
	}
	if msg, err := d.Tweak("kill_bg"); err != nil || !strings.Contains(msg, "освобождено") {
		t.Fatalf("kill_bg: %q %v", msg, err)
	}
	if msg, err := d.Tweak("trim_caches"); err != nil || !strings.Contains(msg, "освобождено") {
		t.Fatalf("trim_caches: %q %v", msg, err)
	}
	if err := d.ForceStop("com.netflix.ninja"); err != nil {
		t.Fatal(err)
	}

	// Upload and install: a big file crosses many ADB packets.
	big := append([]byte("PK\x03\x04"), bytes.Repeat([]byte("unbrake"), 150_000)...)
	var last int64
	if err := d.InstallAPK(bytes.NewReader(big), func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if last != int64(len(big)) {
		t.Fatalf("progress stopped at %d of %d", last, len(big))
	}
	if err := d.InstallAPK(strings.NewReader("this is not an apk"), nil); err == nil || !strings.Contains(err.Error(), "не APK") {
		t.Fatalf("a broken file must be refused with a clear message: %v", err)
	}

	// A new launcher appears and becomes the default one.
	if err := d.InstallAPK(strings.NewReader(FakeAPKPrefix+"com.example.demohome;launcher\n"), nil); err != nil {
		t.Fatal(err)
	}
	all, def := d.Launchers()
	if len(all) != 3 || def != "com.mitv.tvhome.atv" {
		t.Fatalf("launchers: %v %s", all, def)
	}
	if err := d.SetHome("com.example.demohome"); err != nil {
		t.Fatal(err)
	}
	if _, def = d.Launchers(); def != "com.example.demohome" {
		t.Fatalf("default launcher: %s", def)
	}
	if err := d.SetHome("com.netflix.ninja"); err == nil {
		t.Fatal("an ordinary app cannot become the home screen")
	}
	if err := d.OpenStore("com.example.demohome"); err != nil {
		t.Fatal(err)
	}

	// The connection survived everything above and a known key reconnects silently.
	if out, err := c.Shell("pm list packages --user 0 -3", 5*time.Second); err != nil || !strings.Contains(out, "com.example.demohome") {
		t.Fatalf("connection is broken after uploads: %v", err)
	}
	c.Close()
	if c.Alive() {
		t.Fatal("closed connection reports alive")
	}
	prompted = false
	c2, err := adb.Dial(addr, k, 5*time.Second, func() { prompted = true })
	if err != nil || prompted {
		t.Fatalf("known key must be accepted without a dialog: %v", err)
	}
	c2.Close()
}

// Close must interrupt a command that is waiting for the device.
func TestCloseInterruptsShell(t *testing.T) {
	_, addr := start(t, Options{})
	c, err := adb.Dial(addr, key(t), 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		// The emulated TV takes 20 seconds to answer this one.
		_, err := c.Shell("sleep 20", 30*time.Second)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if !c.Alive() {
		t.Fatal("Alive must not block or fail while a command runs")
	}
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an interrupted command must return an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not interrupt the running command")
	}
}

// Wireless debugging (Android 11+): TLS, pairing by code.
func TestWirelessPairing(t *testing.T) {
	f, addr := start(t, Options{Wireless: true, PairCode: "482913"})
	pairAddr, err := f.ListenPairing("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	k := key(t)

	if !adb.Probe(context.Background(), addr, time.Second) {
		t.Fatal("Probe must recognise a wireless-debugging TV")
	}
	if _, err := adb.Dial(addr, k, 3*time.Second, nil); !errors.Is(err, adb.ErrNeedsPairing) {
		t.Fatalf("an unpaired computer must be told to pair, got: %v", err)
	}
	if err := adb.Pair(pairAddr, "000000", k, 5*time.Second); !errors.Is(err, adb.ErrBadPairingCode) {
		t.Fatalf("a wrong code must be reported as such, got: %v", err)
	}
	if _, err := adb.Dial(addr, k, 3*time.Second, nil); !errors.Is(err, adb.ErrNeedsPairing) {
		t.Fatalf("a failed pairing must not grant access: %v", err)
	}
	// Pairing on the wrong port (the ADB port itself) gives an error, not a hang.
	if err := adb.Pair(addr, "482913", k, 3*time.Second); err == nil {
		t.Fatal("pairing against a non-pairing port must fail")
	}
	if err := adb.Pair(pairAddr, "482913", k, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	c, err := adb.Dial(addr, k, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.TLS {
		t.Fatal("the connection must be encrypted")
	}
	d := tv.New(c)
	info, err := d.Info()
	if err != nil || !info.Wireless || info.Model != "Mi TV A2 43" {
		t.Fatalf("info over TLS: %+v %v", info, err)
	}
	if pk, err := d.Packages(); err != nil || len(pk) < 100 {
		t.Fatalf("packages over TLS: %d %v", len(pk), err)
	}
	if err := d.InstallAPK(bytes.NewReader(append([]byte("PK\x03\x04"), make([]byte, 300_000)...)), nil); err != nil {
		t.Fatalf("upload over TLS: %v", err)
	}
	// Another computer is still a stranger.
	if _, err := adb.Dial(addr, key(t), 3*time.Second, nil); !errors.Is(err, adb.ErrNeedsPairing) {
		t.Fatalf("a different key must not be accepted: %v", err)
	}
}
