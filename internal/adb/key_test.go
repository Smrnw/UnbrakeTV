package adb

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"
)

func TestAndroidPublicKey(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	out, err := AndroidPublicKey(&k.PublicKey, "me@pc")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasSuffix(s, " me@pc\x00") {
		t.Fatalf("bad suffix: %q", s[len(s)-10:])
	}
	raw, err := base64.StdEncoding.DecodeString(strings.SplitN(s, " ", 2)[0])
	if err != nil || len(raw) != 524 {
		t.Fatalf("len=%d err=%v", len(raw), err)
	}
	n0 := uint32(new(big.Int).Mod(k.N, big.NewInt(1<<32)).Uint64())
	if n0*binary.LittleEndian.Uint32(raw[4:]) != 0xffffffff {
		t.Fatal("n0inv is wrong")
	}
	if binary.LittleEndian.Uint32(raw[520:]) != 65537 {
		t.Fatal("exponent is wrong")
	}
}
