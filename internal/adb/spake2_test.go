package adb

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The coordinates are quoted from BoringSSL's spake25519.cc; decoding our
// constants must give exactly these points.
func TestSpakePoints(t *testing.T) {
	cases := []struct {
		name string
		p    edPoint
		x, y string
	}{
		{"M", spakeM,
			"31406539342727633121250288103050113562375374900226415211311216773867585644232",
			"21177308356423958466833845032658859666296341766942662650232962324899758529114"},
		{"N", spakeN,
			"49918732221787544735331783592030787422991506689877079631459872391322455579424",
			"54629554431565467720832445949441049581317094546788069926228343916274969994000"},
	}
	for _, c := range cases {
		if c.p.x.String() != c.x || c.p.y.String() != c.y {
			t.Errorf("%s decoded to a wrong point", c.name)
		}
	}
	// The base point must have the prime order: l·B is the neutral element.
	if o := edBase.mul(edL); o.x.Sign() != 0 || o.y.Cmp(bigOne()) != 0 {
		t.Error("l·B is not the neutral element")
	}
	// Round trip of the standard base point encoding.
	want := "5866666666666666666666666666666666666666666666666666666666666666"
	if got := hexOf(edBase.encode()); got != want {
		t.Errorf("base point encodes to %s", got)
	}
}

func TestSpake2Agreement(t *testing.T) {
	pw := []byte("123456 some exported key material")
	a, err := NewSpake2(true, pw)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSpake2(false, pw)
	if a.pwScalar.Bit(0)+a.pwScalar.Bit(1)+a.pwScalar.Bit(2) != 0 {
		t.Fatal("password scalar must be divisible by eight")
	}
	ka, err := a.Finish(b.Msg())
	if err != nil {
		t.Fatal(err)
	}
	kb, err := b.Finish(a.Msg())
	if err != nil {
		t.Fatal(err)
	}
	if len(ka) != 64 || !bytes.Equal(ka, kb) {
		t.Fatal("both sides must derive the same key")
	}

	c, _ := NewSpake2(false, []byte("654321 some exported key material"))
	kc, _ := c.Finish(a.Msg())
	a2, _ := NewSpake2(true, pw)
	ka2, _ := a2.Finish(c.Msg())
	if bytes.Equal(ka2, kc) {
		t.Fatal("a wrong code must not produce a shared key")
	}
	if _, err := a.Finish(make([]byte, 31)); err == nil {
		t.Fatal("short message must be rejected")
	}
}

func TestPairingCipher(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 64)
	a, _ := NewPairingCipher(secret)
	b, _ := NewPairingCipher(secret)
	msg := PeerInfo([]byte("key user@host\x00"))
	if len(msg) != PeerInfoSize {
		t.Fatal("PeerInfo size")
	}
	got, err := b.Open(a.Seal(msg))
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatal("round trip failed")
	}
	// The nonce is a counter: replaying the first packet must fail.
	first := a.Seal(msg)
	b.Open(first)
	if _, err := b.Open(first); err == nil {
		t.Fatal("replay must fail")
	}

	var buf bytes.Buffer
	WritePairingPacket(&buf, 1, []byte("abc"))
	if hdr := buf.Bytes()[:6]; hdr[0] != 1 || hdr[1] != 1 || binary.BigEndian.Uint32(hdr[2:]) != 3 {
		t.Fatalf("bad header % x", hdr)
	}
	if p, err := ReadPairingPacket(&buf, 1); err != nil || string(p) != "abc" {
		t.Fatal("packet round trip failed")
	}
}
