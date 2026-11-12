package tv

import "testing"

func TestPresets(t *testing.T) {
	list, suggested := Presets("Xiaomi")
	if suggested != "xiaomi-ads" || len(list) < 5 {
		t.Fatalf("Xiaomi: %s, %d presets", suggested, len(list))
	}
	for m, want := range map[string]string{"Sony": "safe", "": "safe", "MI": "xiaomi-ads", "Redmi": "xiaomi-ads", "Philips": "safe", "Xiaomi ": "xiaomi-ads"} {
		if _, got := Presets(m); got != want {
			t.Errorf("%q: suggested %s, want %s", m, got, want)
		}
	}
	ids := map[string]bool{}
	for _, p := range list {
		if ids[p.ID] || p.Title == "" || p.Description == "" {
			t.Errorf("preset %q is duplicated or incomplete", p.ID)
		}
		ids[p.ID] = true
		for _, pkg := range p.Packages {
			// A ready-made profile must never tick something that can break the TV.
			if lv := Lookup(pkg, false).Level; lv == Critical {
				t.Errorf("preset %s contains critical package %s", p.ID, pkg)
			}
		}
	}
	safe := list[0]
	if !safe.Match("com.vendor.whatever", Recommended) || safe.Match("com.vendor.whatever", Caution) {
		t.Error("the safe preset is exactly the recommended level")
	}
	for _, p := range list {
		if p.ID == "retail" && (!p.Match("com.android.cts.ctsshim", Unknown) || p.Match("com.android.settings", Critical)) {
			t.Error("wildcard matching in presets")
		}
	}
}

func TestVendorTelemetryHint(t *testing.T) {
	cases := map[string]Level{
		"com.xiaomi.mitv.analytics.service": Recommended, // vendor wildcard + telling name
		"com.mitv.adverts.loader":           Recommended,
		"com.xiaomi.mitv.settings":          Unknown,
		"com.google.android.gms":            Critical, // exact rules are never downgraded
		"com.android.providers.telemetry":   Critical,
		"com.android.retaildemo":            Recommended,
		"com.spocky.projengmenu":            Optional,
	}
	for p, want := range cases {
		if got := Lookup(p, false).Level; got != want {
			t.Errorf("%s: got %s, want %s", p, got, want)
		}
	}
	// An app the user installed is never "recommended" just because of its name.
	if got := Lookup("com.xiaomi.demo.game", true).Level; got != Optional {
		t.Errorf("third-party app: %s", got)
	}
}
