// Package tv contains Android TV operations on top of an ADB connection.
package tv

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Smrnw/UnbrakeTV/internal/adb"
)

// Device wraps an ADB connection to one TV.
type Device struct {
	Conn *adb.Conn

	mu      sync.Mutex
	prevCPU []uint64
	sizes   map[string]int64
	sizesAt time.Time
	pkgs    map[string]bool // all package names, from the last Packages() call
	caches  map[string]int64
	storage Storage

	// SDK is the Android API level, filled in by Info().
	SDK int
}

func New(c *adb.Conn) *Device { return &Device{Conn: c} }

func (d *Device) sh(cmd string) (string, error) {
	return d.Conn.Shell(cmd, 60*time.Second)
}

// DeviceInfo is basic information about the TV.
type DeviceInfo struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Android      string `json:"android"`
	SDK          int    `json:"sdk"`
	ABI          string `json:"abi"`
	Serial       string `json:"serial"`
	Address      string `json:"address"`
	IsTV         bool   `json:"isTv"`
	Wireless     bool   `json:"wireless"` // connected through "Отладка по Wi-Fi" (TLS)
}

func (d *Device) Info() (DeviceInfo, error) {
	out, err := d.sh("getprop ro.product.manufacturer; getprop ro.product.model; getprop ro.build.version.release; getprop ro.build.version.sdk; getprop ro.product.cpu.abi; getprop ro.serialno; pm list features 2>/dev/null | grep -c -E 'leanback|android.software.leanback|com.google.android.tv'")
	if err != nil {
		return DeviceInfo{}, err
	}
	l := lines(out)
	for len(l) < 7 {
		l = append(l, "")
	}
	sdk, _ := strconv.Atoi(l[3])
	n, _ := strconv.Atoi(l[6])
	d.SDK = sdk
	return DeviceInfo{Manufacturer: l[0], Model: l[1], Android: l[2], SDK: sdk, ABI: l[4], Serial: l[5], Address: d.Conn.Addr(), IsTV: n > 0, Wireless: d.Conn.TLS}, nil
}

func lines(s string) []string {
	s = strings.ReplaceAll(s, "\r", "")
	var out []string
	for _, x := range strings.Split(s, "\n") {
		out = append(out, strings.TrimSpace(x))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func pkgSet(out string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lines(out) {
		if p, ok := strings.CutPrefix(l, "package:"); ok && p != "" {
			// "-f" format is "package:/path/base.apk=name"; plain is "package:name"
			if i := strings.LastIndex(p, "="); i >= 0 {
				p = p[i+1:]
			}
			m[p] = true
		}
	}
	return m
}

// Package is one app on the TV.
type Package struct {
	ID string `json:"id"`
	Info
	System            bool    `json:"system"`
	State             string  `json:"state"` // installed | disabled | removed
	SizeBytes         int64   `json:"sizeBytes"`
	CacheBytes        int64   `json:"cacheBytes"`
	MemKB             int64   `json:"memKb"`
	CPU               float64 `json:"cpu"`
	Running           bool    `json:"running"`
	IsLauncher        bool    `json:"isLauncher"`
	IsDefaultLauncher bool    `json:"isDefaultLauncher"`
	IsKeyboard        bool    `json:"isKeyboard"`
}

// Launchers returns the packages that can act as a home screen, plus the current default.
func (d *Device) Launchers() (all []string, def string) {
	out, _ := d.sh("cmd package query-activities --brief -a android.intent.action.MAIN -c android.intent.category.HOME 2>/dev/null")
	seen := map[string]bool{}
	for _, l := range lines(out) {
		if i := strings.Index(l, "/"); i > 0 && !strings.Contains(l, " ") {
			p := l[:i]
			// Settings' fallback home is not a real launcher.
			if p == "com.android.settings" || p == "com.android.tv.settings" || seen[p] {
				continue
			}
			seen[p] = true
			all = append(all, p)
		}
	}
	out, _ = d.sh("cmd package resolve-activity --brief -a android.intent.action.MAIN -c android.intent.category.HOME 2>/dev/null")
	for _, l := range lines(out) {
		if i := strings.Index(l, "/"); i > 0 && !strings.Contains(l, " ") {
			def = l[:i]
		}
	}
	return
}

func (d *Device) keyboards() map[string]bool {
	out, _ := d.sh("ime list -s 2>/dev/null")
	m := map[string]bool{}
	for _, l := range lines(out) {
		if i := strings.Index(l, "/"); i > 0 {
			m[l[:i]] = true
		}
	}
	return m
}

// Packages lists all packages including ones removed for the user.
func (d *Device) Packages() ([]Package, error) {
	all, err := d.sh("pm list packages --user 0 -u")
	if err != nil {
		return nil, err
	}
	if strings.Contains(all, "Error") && !strings.Contains(all, "package:") {
		return nil, errors.New(strings.TrimSpace(all))
	}
	installedOut, err := d.sh("pm list packages --user 0")
	if err != nil {
		return nil, err
	}
	disabledOut, _ := d.sh("pm list packages --user 0 -d")
	thirdOut, _ := d.sh("pm list packages --user 0 -3")
	allSet, installed, disabled, third := pkgSet(all), pkgSet(installedOut), pkgSet(disabledOut), pkgSet(thirdOut)
	d.mu.Lock()
	d.pkgs = allSet
	d.mu.Unlock()

	launchers, def := d.Launchers()
	lset := map[string]bool{}
	for _, l := range launchers {
		lset[l] = true
	}
	kbs := d.keyboards()
	sizes := d.packageSizes()
	procs := d.processUsage()
	// Fold processes like "com.google.android.gms.persistent" into their package.
	for name, u := range procs {
		if allSet[name] {
			continue
		}
		best := ""
		for p := range allSet {
			if strings.HasPrefix(name, p+".") && len(p) > len(best) {
				best = p
			}
		}
		if best != "" {
			b := procs[best]
			b.memKB += u.memKB
			b.cpu += u.cpu
			procs[best] = b
		}
	}

	var res []Package
	for p := range allSet {
		st := "installed"
		if !installed[p] {
			st = "removed"
		} else if disabled[p] {
			st = "disabled"
		}
		inf := Lookup(p, third[p])
		pk := Package{ID: p, Info: inf, System: !third[p], State: st, SizeBytes: sizes[p], CacheBytes: d.cacheOf(p)}
		if u, ok := procs[p]; ok {
			pk.MemKB, pk.CPU, pk.Running = u.memKB, u.cpu, true
		}
		pk.IsLauncher = lset[p]
		pk.IsDefaultLauncher = p == def
		pk.IsKeyboard = kbs[p]
		if pk.IsDefaultLauncher && pk.Level != Critical {
			if len(launchers) <= 1 {
				pk.Level = Critical
				pk.Reason = "Это единственный рабочий стол. Если удалить — после кнопки «Домой» будет пустой экран. " + inf.Reason
			} else {
				pk.Level = Caution
				pk.Reason = "Текущий рабочий стол. После удаления включится другой. " + inf.Reason
			}
		}
		if pk.IsKeyboard && len(kbs) <= 1 && pk.Level != Critical {
			pk.Level = Critical
			pk.Reason = "Единственная экранная клавиатура — без неё нельзя будет вводить текст."
		}
		res = append(res, pk)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	return res, nil
}

var bracketList = regexp.MustCompile(`^(Package Names|App Sizes|App Data Sizes|Cache Sizes):\s*\[(.*)\]`)

// packageSizes parses `dumpsys diskstats` (cached for a minute; it is slow).
func (d *Device) packageSizes() map[string]int64 {
	d.mu.Lock()
	if d.sizes != nil && time.Since(d.sizesAt) < time.Minute {
		s := d.sizes
		d.mu.Unlock()
		return s
	}
	d.mu.Unlock()
	out, _ := d.sh("dumpsys diskstats 2>/dev/null")
	var names []string
	var app, data, cache []int64
	for _, l := range lines(out) {
		m := bracketList.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		items := strings.Split(m[2], ",")
		switch m[1] {
		case "Package Names":
			for _, it := range items {
				names = append(names, strings.Trim(strings.TrimSpace(it), `"`))
			}
		default:
			var v []int64
			for _, it := range items {
				n, _ := strconv.ParseInt(strings.TrimSpace(it), 10, 64)
				v = append(v, n)
			}
			switch m[1] {
			case "App Sizes":
				app = v
			case "App Data Sizes":
				data = v
			case "Cache Sizes":
				cache = v
			}
		}
	}
	res := map[string]int64{}
	at := func(v []int64, i int) int64 {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	caches := map[string]int64{}
	for i, n := range names {
		res[n] = at(app, i) + at(data, i) + at(cache, i)
		caches[n] = at(cache, i)
	}
	d.mu.Lock()
	d.sizes, d.sizesAt, d.caches, d.storage = res, time.Now(), caches, parseStorage(out)
	d.mu.Unlock()
	return res
}

func (d *Device) cacheOf(pkg string) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.caches[pkg]
}

type usage struct {
	memKB int64
	cpu   float64
}

var pkgLike = regexp.MustCompile(`^[a-zA-Z][\w]*(\.[\w]+)+`)

// processUsage aggregates RSS memory and CPU per package from top/ps.
func (d *Device) processUsage() map[string]usage {
	res := map[string]usage{}
	out, err := d.sh("top -b -n 1 -q -o %CPU,RES,NAME 2>/dev/null")
	parsed := false
	if err == nil {
		for _, l := range lines(out) {
			f := strings.Fields(l)
			if len(f) < 3 {
				continue
			}
			cpu, err1 := strconv.ParseFloat(f[0], 64)
			mem, ok := parseMem(f[1])
			if err1 != nil || !ok {
				continue
			}
			parsed = true
			add(res, f[len(f)-1], mem, cpu)
		}
	}
	if !parsed {
		out, _ = d.sh("ps -A -o RSS,NAME 2>/dev/null")
		for _, l := range lines(out) {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			mem, err := strconv.ParseInt(f[0], 10, 64)
			if err != nil {
				continue
			}
			add(res, f[len(f)-1], mem, 0)
		}
	}
	return res
}

func add(m map[string]usage, name string, memKB int64, cpu float64) {
	pkg := name
	if i := strings.Index(pkg, ":"); i > 0 {
		pkg = pkg[:i]
	}
	if !pkgLike.MatchString(pkg) {
		return
	}
	u := m[pkg]
	u.memKB += memKB
	u.cpu += cpu
	m[pkg] = u
}

// parseMem understands "123456", "120M", "1.2G", "900K".
func parseMem(s string) (int64, bool) {
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1024*1024, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1024, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		s = strings.TrimSuffix(s, "K")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int64(v * mult), true
}

// Proc is a process entry for the live load view.
type Proc struct {
	Package string  `json:"package"`
	Name    string  `json:"name"`
	MemKB   int64   `json:"memKb"`
	CPU     float64 `json:"cpu"`
	Level   Level   `json:"level"`
}

// Stats is a snapshot of the TV load.
type Stats struct {
	CPU         float64 `json:"cpu"`
	MemTotalKB  int64   `json:"memTotalKb"`
	MemAvailKB  int64   `json:"memAvailKb"`
	StorTotalKB int64   `json:"storTotalKb"`
	StorUsedKB  int64   `json:"storUsedKb"`
	TempC       float64 `json:"tempC"`
	UptimeSec   int64   `json:"uptimeSec"`
	Top         []Proc  `json:"top"`
	Time        int64   `json:"time"`
}

func (d *Device) Stats() (Stats, error) {
	out, err := d.sh("head -1 /proc/stat; echo '@@@'; cat /proc/meminfo; echo '@@@'; df -k /data 2>/dev/null || df /data; echo '@@@'; cat /sys/class/thermal/thermal_zone*/temp 2>/dev/null; echo '@@@'; top -b -n 1 -q -o %CPU,RES,NAME 2>/dev/null; echo '@@@'; cat /proc/uptime 2>/dev/null")
	if err != nil {
		return Stats{}, err
	}
	parts := strings.Split(out, "@@@")
	for len(parts) < 6 {
		parts = append(parts, "")
	}
	s := Stats{Time: time.Now().UnixMilli()}
	if f := strings.Fields(parts[5]); len(f) > 0 {
		up, _ := strconv.ParseFloat(f[0], 64)
		s.UptimeSec = int64(up)
	}

	// CPU from /proc/stat delta between two polls
	f := strings.Fields(parts[0])
	if len(f) > 4 && f[0] == "cpu" {
		var vals []uint64
		for _, x := range f[1:] {
			v, _ := strconv.ParseUint(x, 10, 64)
			vals = append(vals, v)
		}
		d.mu.Lock()
		prev := d.prevCPU
		d.prevCPU = vals
		d.mu.Unlock()
		if len(prev) == len(vals) {
			var tot, idle uint64
			for i := range vals {
				tot += vals[i] - prev[i]
			}
			idle = vals[3] - prev[3]
			if len(vals) > 4 {
				idle += vals[4] - prev[4]
			}
			if tot > 0 {
				s.CPU = 100 * float64(tot-idle) / float64(tot)
			}
		}
	}
	for _, l := range lines(parts[1]) {
		ff := strings.Fields(l)
		if len(ff) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(ff[1], 10, 64)
		switch ff[0] {
		case "MemTotal:":
			s.MemTotalKB = v
		case "MemAvailable:":
			s.MemAvailKB = v
		}
	}
	dl := lines(parts[2])
	if len(dl) >= 2 {
		ff := strings.Fields(dl[len(dl)-1])
		if len(ff) >= 4 {
			t, ok1 := parseDf(ff[1])
			u, ok2 := parseDf(ff[2])
			if ok1 && ok2 {
				s.StorTotalKB, s.StorUsedKB = t, u
			}
		}
	}
	// Several sensors: show the hottest plausible one.
	for _, l := range lines(parts[3]) {
		t, err := strconv.ParseFloat(l, 64)
		if err != nil {
			continue
		}
		if t > 1000 {
			t /= 1000
		}
		if t >= 15 && t <= 125 && t > s.TempC {
			s.TempC = t
		}
	}
	d.mu.Lock()
	known := d.pkgs
	d.mu.Unlock()
	agg := map[string]*Proc{}
	for _, l := range lines(parts[4]) {
		ff := strings.Fields(l)
		if len(ff) < 3 {
			continue
		}
		cpu, err1 := strconv.ParseFloat(ff[0], 64)
		mem, ok := parseMem(ff[1])
		if err1 != nil || !ok {
			continue
		}
		name := ff[len(ff)-1]
		pkg := name
		if i := strings.Index(pkg, ":"); i > 0 {
			pkg = pkg[:i]
		}
		if !pkgLike.MatchString(pkg) {
			continue
		}
		// Fold processes like "com.google.android.gms.persistent" into their package.
		if len(known) > 0 && !known[pkg] {
			for q := pkg; ; {
				i := strings.LastIndex(q, ".")
				if i < 0 {
					break
				}
				if q = q[:i]; known[q] {
					pkg = q
					break
				}
			}
		}
		p := agg[pkg]
		if p == nil {
			inf := Lookup(pkg, false)
			p = &Proc{Package: pkg, Name: inf.Name, Level: inf.Level}
			agg[pkg] = p
		}
		p.CPU += cpu
		p.MemKB += mem
	}
	for _, p := range agg {
		s.Top = append(s.Top, *p)
	}
	sort.Slice(s.Top, func(i, j int) bool {
		if s.Top[i].CPU != s.Top[j].CPU {
			return s.Top[i].CPU > s.Top[j].CPU
		}
		return s.Top[i].MemKB > s.Top[j].MemKB
	})
	if len(s.Top) > 12 {
		s.Top = s.Top[:12]
	}
	return s, nil
}

// parseDf understands plain KB numbers and human-readable values like "12G".
func parseDf(s string) (int64, bool) {
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v, true
	}
	return parseMem(s)
}

// Result is the outcome for one package.
type Result struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Method string `json:"method"`
	Output string `json:"output"`
}

// Remove uninstalls packages for user 0, falling back to disabling them.
func (d *Device) Remove(pkgs []string) []Result {
	var res []Result
	for _, p := range pkgs {
		if !validPkg(p) {
			res = append(res, Result{ID: p, Output: "некорректное имя пакета"})
			continue
		}
		out, err := d.sh("pm uninstall -k --user 0 " + p)
		out = strings.TrimSpace(out)
		if err == nil && strings.Contains(out, "Success") {
			res = append(res, Result{ID: p, OK: true, Method: "uninstall", Output: out})
			continue
		}
		out2, err2 := d.sh("pm disable-user --user 0 " + p)
		out2 = strings.TrimSpace(out2)
		if err2 == nil && strings.Contains(out2, "disabled") {
			res = append(res, Result{ID: p, OK: true, Method: "disable", Output: out2})
			continue
		}
		res = append(res, Result{ID: p, Output: fmt.Sprintf("%s / %s", out, out2)})
	}
	d.mu.Lock()
	d.sizes = nil
	d.mu.Unlock()
	return res
}

// Restore brings back packages removed or disabled earlier.
func (d *Device) Restore(pkgs []string) []Result {
	var res []Result
	for _, p := range pkgs {
		if !validPkg(p) {
			res = append(res, Result{ID: p, Output: "некорректное имя пакета"})
			continue
		}
		out, _ := d.sh("cmd package install-existing --user 0 " + p + " 2>&1 || pm install-existing --user 0 " + p)
		out2, _ := d.sh("pm enable --user 0 " + p + " 2>&1")
		all := strings.TrimSpace(out + "\n" + out2)
		ok := strings.Contains(out, "installed for user") || strings.Contains(out2, "enabled")
		res = append(res, Result{ID: p, OK: ok, Method: "restore", Output: all})
	}
	// Trust the real state more than the command output.
	if insOut, err := d.sh("pm list packages --user 0"); err == nil && strings.Contains(insOut, "package:") {
		disOut, _ := d.sh("pm list packages --user 0 -d")
		installed, disabled := pkgSet(insOut), pkgSet(disOut)
		for i := range res {
			if validPkg(res[i].ID) {
				res[i].OK = installed[res[i].ID] && !disabled[res[i].ID]
			}
		}
	}
	d.mu.Lock()
	d.sizes = nil
	d.mu.Unlock()
	return res
}

var validRe = regexp.MustCompile(`^[a-zA-Z0-9_]+(\.[a-zA-Z0-9_]+)*$`)

func validPkg(p string) bool { return len(p) < 256 && validRe.MatchString(p) }

// ValidPackage reports whether p is a syntactically valid package name.
func ValidPackage(p string) bool { return validPkg(p) }

// TweakState is the current state of quick tweaks.
type TweakState struct {
	AnimScale    string `json:"animScale"`    // "1", "0.5", "0" or "mixed"
	ProcLimit    string `json:"procLimit"`    // "default", a number, or "unsupported"
	Screensaver  bool   `json:"screensaver"`  // the idle screensaver is enabled
	DNS          string `json:"dns"`          // filtering DNS host, "" when off
	DNSSupported bool   `json:"dnsSupported"` // Private DNS exists on this Android
}

var animKeys = []string{"window_animation_scale", "transition_animation_scale", "animator_duration_scale"}

func (d *Device) Tweaks() TweakState {
	t := TweakState{AnimScale: d.animScale()}
	if extraTweakState != nil {
		extraTweakState(d, &t)
	}
	return t
}

func (d *Device) animScale() string {
	vals := map[string]bool{}
	for _, k := range animKeys {
		out, _ := d.sh("settings get global " + k)
		v := strings.TrimSpace(out)
		if v == "" || v == "null" {
			v = "1"
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			v = strconv.FormatFloat(f, 'f', -1, 64)
		}
		vals[v] = true
	}
	if len(vals) == 1 {
		for v := range vals {
			return v
		}
	}
	return "mixed"
}

// Tweak applies a quick, reversible optimisation.
func (d *Device) Tweak(action string) (string, error) {
	setAnim := func(v string) (string, error) {
		var cmds []string
		for _, k := range animKeys {
			cmds = append(cmds, "settings put global "+k+" "+v)
		}
		_, err := d.sh(strings.Join(cmds, "; "))
		return "Скорость анимаций изменена", err
	}
	if f, ok := extraTweakActions[action]; ok {
		return f(d)
	}
	switch action {
	case "anim_fast":
		return setAnim("0.5")
	case "anim_off":
		return setAnim("0")
	case "anim_default":
		return setAnim("1")
	case "trim_caches":
		before, _ := d.freeDataKB()
		out, err := d.Conn.Shell("pm trim-caches 999G", 3*time.Minute)
		if err != nil {
			return "", err
		}
		after, _ := d.freeDataKB()
		if strings.Contains(out, "Error") || strings.Contains(out, "Exception") {
			return "", errors.New(strings.TrimSpace(out))
		}
		if after > before {
			return fmt.Sprintf("Кэш очищен, освобождено %d МБ", (after-before)/1024), nil
		}
		return "Кэш очищен", nil
	case "kill_bg":
		before := d.memAvailKB()
		out, err := d.sh("am kill-all 2>&1")
		if err != nil {
			return "", err
		}
		if strings.Contains(out, "Exception") || strings.Contains(out, "Error") {
			return "", errors.New(strings.TrimSpace(out))
		}
		time.Sleep(1200 * time.Millisecond)
		if after := d.memAvailKB(); before > 0 && after-before >= 1024 {
			return fmt.Sprintf("Фоновые приложения закрыты, освобождено %d МБ оперативной памяти", (after-before)/1024), nil
		}
		return "Фоновые приложения закрыты", nil
	case "reboot":
		d.Conn.Shell("reboot", 5*time.Second)
		return "Телевизор перезагружается", nil
	case "adb_off":
		d.Conn.Shell("settings put global adb_enabled 0", 5*time.Second)
		return "Отладка выключена", nil
	}
	return "", errors.New("неизвестное действие")
}

func (d *Device) freeDataKB() (int64, error) {
	out, err := d.sh("df -k /data")
	if err != nil {
		return 0, err
	}
	l := lines(out)
	if len(l) < 2 {
		return 0, errors.New("df")
	}
	f := strings.Fields(l[len(l)-1])
	if len(f) < 4 {
		return 0, errors.New("df")
	}
	v, ok := parseDf(f[3])
	if !ok {
		return 0, errors.New("df")
	}
	return v, nil
}

// Disable only freezes packages (pm disable-user) — the most conservative option.
func (d *Device) Disable(pkgs []string) []Result {
	var res []Result
	for _, p := range pkgs {
		if !validPkg(p) {
			res = append(res, Result{ID: p, Output: "некорректное имя пакета"})
			continue
		}
		out, err := d.sh("pm disable-user --user 0 " + p)
		out = strings.TrimSpace(out)
		res = append(res, Result{ID: p, OK: err == nil && strings.Contains(out, "disabled"), Method: "disable", Output: out})
	}
	return res
}

func (d *Device) memAvailKB() int64 {
	out, err := d.sh("grep MemAvailable /proc/meminfo")
	if err != nil {
		return 0
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(f[1], 10, 64)
	return v
}

// ForceStop stops one app until it is launched again.
func (d *Device) ForceStop(pkg string) error {
	if !validPkg(pkg) {
		return errors.New("некорректное имя пакета")
	}
	out, err := d.sh("am force-stop " + pkg + " 2>&1")
	if err != nil {
		return err
	}
	if strings.Contains(out, "Exception") || strings.Contains(out, "Error") {
		return errors.New(strings.TrimSpace(out))
	}
	return nil
}

var installErrors = []struct{ code, text string }{
	{"NO_MATCHING_ABIS", "приложение собрано для другого процессора и не подходит этому телевизору"},
	{"OLDER_SDK", "приложению нужна более новая версия Android, чем на телевизоре"},
	{"INSUFFICIENT_STORAGE", "на телевизоре не хватает свободной памяти"},
	{"UPDATE_INCOMPATIBLE", "на телевизоре уже стоит версия с другой подписью — сначала удалите её"},
	{"VERSION_DOWNGRADE", "на телевизоре уже стоит более новая версия"},
	{"PARSE_FAILED", "файл повреждён или это не APK"},
	{"INVALID_APK", "файл повреждён или это не APK"},
	{"VERIFICATION_FAILURE", "установку заблокировала проверка приложений на телевизоре"},
	{"USER_RESTRICTED", "установка через ADB запрещена в настройках телевизора"},
}

// InstallAPK uploads an APK and installs it. progress receives bytes sent.
func (d *Device) InstallAPK(r io.Reader, progress func(sent int64)) error {
	const tmp = "/data/local/tmp/unbrake_install.apk"
	if err := d.Conn.Push(tmp, 0o644, r, progress); err != nil {
		return err
	}
	out, err := d.Conn.Shell("pm install -r "+tmp+" 2>&1", 5*time.Minute)
	d.sh("rm -f " + tmp)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.sizes = nil
	d.mu.Unlock()
	out = strings.TrimSpace(out)
	if strings.Contains(out, "Success") {
		return nil
	}
	for _, e := range installErrors {
		if strings.Contains(out, e.code) {
			return errors.New("Не удалось установить: " + e.text + ".")
		}
	}
	if len(out) > 300 {
		out = out[:300]
	}
	return errors.New("Не удалось установить: " + out)
}

// SetHome makes pkg the default home screen and shows it.
func (d *Device) SetHome(pkg string) error {
	if !validPkg(pkg) {
		return errors.New("некорректное имя пакета")
	}
	out, err := d.sh("cmd package query-activities --brief -a android.intent.action.MAIN -c android.intent.category.HOME 2>/dev/null")
	if err != nil {
		return err
	}
	comp := ""
	for _, l := range lines(out) {
		if strings.HasPrefix(l, pkg+"/") && !strings.Contains(l, " ") {
			comp = l
			break
		}
	}
	if comp == "" {
		return errors.New("это приложение не установлено или не умеет быть рабочим столом")
	}
	out, err = d.sh("cmd package set-home-activity " + comp + " 2>&1")
	if err != nil {
		return err
	}
	if !strings.Contains(out, "Success") {
		return errors.New("Прошивка не разрешила сменить рабочий стол командой. Нажмите «Домой» на пульте и выберите новый рабочий стол, либо отключите стандартный на вкладке «Приложения».")
	}
	d.sh("am start -a android.intent.action.MAIN -c android.intent.category.HOME 2>&1")
	return nil
}

// OpenStore opens the Play Store page of pkg on the TV screen.
func (d *Device) OpenStore(pkg string) error {
	if !validPkg(pkg) {
		return errors.New("некорректное имя пакета")
	}
	out, err := d.sh("am start -a android.intent.action.VIEW -d 'market://details?id=" + pkg + "' 2>&1")
	if err != nil {
		return err
	}
	if strings.Contains(out, "Error") || strings.Contains(out, "Exception") {
		return errors.New("На телевизоре не открылся Play Маркет — возможно, он удалён или отключён.")
	}
	return nil
}

// Optional tweaks register themselves here, so that the core list above
// stays small and each addition lives in its own file.
var (
	extraTweakState   func(d *Device, t *TweakState)
	extraTweakActions = map[string]func(d *Device) (string, error){}
)
