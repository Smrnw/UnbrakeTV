package tv

import "testing"

func TestLookup(t *testing.T) {
	cases := map[string]Level{
		"android":                             Critical,
		"com.google.android.gms":              Critical,
		"com.android.vending":                 Critical,
		"com.android.providers.settings":      Critical,
		"com.mitv.tvhome.atv":                 Caution,
		"com.miui.tv.analytics":               Recommended,
		"com.netflix.ninja":                   Recommended,
		"android.auto_generated_rro_vendor__": Critical,
		"com.vendor.adsanalytics":             Recommended,
		"com.vendor.something":                Unknown,
	}
	for p, want := range cases {
		if got := Lookup(p, false).Level; got != want {
			t.Errorf("%s: got %s, want %s", p, got, want)
		}
	}
	if Lookup("org.example.game", true).Level != Optional {
		t.Error("third-party apps must be optional")
	}
}

func TestParseMem(t *testing.T) {
	for in, want := range map[string]int64{"1024": 1024, "120M": 122880, "1.5G": 1572864, "900K": 900} {
		if got, ok := parseMem(in); !ok || got != want {
			t.Errorf("%s: %d", in, got)
		}
	}
}
