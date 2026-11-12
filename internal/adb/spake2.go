package adb

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"math/big"
)

// This file is a small port of the SPAKE2 variant from BoringSSL
// (crypto/curve25519/spake25519.cc) that adbd uses for pairing by code.
// It is built on math/big to keep the program free of external dependencies;
// it runs a handful of times per pairing, so speed does not matter.

var (
	edP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	edL = func() *big.Int {
		l, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
		return l
	}()
	edD = func() *big.Int {
		// d = -121665/121666 mod p
		d := new(big.Int).ModInverse(big.NewInt(121666), edP)
		d.Mul(d, big.NewInt(-121665))
		return d.Mod(d, edP)
	}()
	edSqrtM1 = new(big.Int).Exp(big.NewInt(2), new(big.Int).Rsh(new(big.Int).Sub(edP, big.NewInt(1)), 2), edP)

	edBase = func() edPoint {
		// y = 4/5, x is the even root.
		y := new(big.Int).ModInverse(big.NewInt(5), edP)
		y.Mul(y, big.NewInt(4)).Mod(y, edP)
		x, _ := edRecoverX(y, 0)
		return edPoint{x, y}
	}()

	// Points from BoringSSL: "edwards25519 point generation seed (M)" / "(N)".
	spakeM = mustPoint("5ada7e4bf6ddd9adb6626d32131c6b5c51a1e347a3478f53cfcf441b88eed12e")
	spakeN = mustPoint("10e3df0ae37d8e7a99b5fe74b44672103dbddcbd06af680d71329a11693bc778")
)

type edPoint struct{ x, y *big.Int }

func mustPoint(hexStr string) edPoint {
	b, ok := new(big.Int).SetString(hexStr, 16)
	if !ok {
		panic("bad point constant")
	}
	p, err := edDecode(b.FillBytes(make([]byte, 32)))
	if err != nil {
		panic(err)
	}
	return p
}

func edRecoverX(y *big.Int, sign uint) (*big.Int, error) {
	// x² = (y² - 1) / (d·y² + 1)
	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, edP)
	num := new(big.Int).Sub(y2, big.NewInt(1))
	den := new(big.Int).Mul(edD, y2)
	den.Add(den, big.NewInt(1)).Mod(den, edP)
	den.ModInverse(den, edP)
	x2 := num.Mul(num, den)
	x2.Mod(x2, edP)

	exp := new(big.Int).Add(edP, big.NewInt(3))
	exp.Rsh(exp, 3)
	x := new(big.Int).Exp(x2, exp, edP)
	chk := new(big.Int).Mul(x, x)
	if chk.Mod(chk, edP).Cmp(x2) != 0 {
		x.Mul(x, edSqrtM1).Mod(x, edP)
		chk.Mul(x, x)
		if chk.Mod(chk, edP).Cmp(x2) != 0 {
			return nil, errors.New("точка не лежит на кривой")
		}
	}
	if x.Sign() == 0 && sign == 1 {
		return nil, errors.New("точка не лежит на кривой")
	}
	if x.Bit(0) != sign {
		x.Sub(edP, x)
	}
	return x, nil
}

func leInt(b []byte) *big.Int {
	r := make([]byte, len(b))
	for i := range b {
		r[len(b)-1-i] = b[i]
	}
	return new(big.Int).SetBytes(r)
}

func edDecode(b []byte) (edPoint, error) {
	if len(b) != 32 {
		return edPoint{}, errors.New("неверная длина точки")
	}
	c := append([]byte(nil), b...)
	sign := uint(c[31] >> 7)
	c[31] &= 0x7f
	y := leInt(c)
	y.Mod(y, edP)
	x, err := edRecoverX(y, sign)
	if err != nil {
		return edPoint{}, err
	}
	return edPoint{x, y}, nil
}

func (p edPoint) encode() []byte {
	be := p.y.FillBytes(make([]byte, 32))
	out := make([]byte, 32)
	for i := range be {
		out[31-i] = be[i]
	}
	out[31] |= byte(p.x.Bit(0)) << 7
	return out
}

func (p edPoint) add(q edPoint) edPoint {
	// Complete twisted Edwards addition (a = -1).
	x1y2 := new(big.Int).Mul(p.x, q.y)
	y1x2 := new(big.Int).Mul(p.y, q.x)
	y1y2 := new(big.Int).Mul(p.y, q.y)
	x1x2 := new(big.Int).Mul(p.x, q.x)
	t := new(big.Int).Mul(x1x2, y1y2)
	t.Mod(t, edP).Mul(t, edD).Mod(t, edP)

	dx := new(big.Int).Add(big.NewInt(1), t)
	dy := new(big.Int).Sub(big.NewInt(1), t)
	dx.Mod(dx, edP).ModInverse(dx, edP)
	dy.Mod(dy, edP).ModInverse(dy, edP)

	x := x1y2.Add(x1y2, y1x2)
	x.Mul(x, dx).Mod(x, edP)
	y := y1y2.Add(y1y2, x1x2)
	y.Mul(y, dy).Mod(y, edP)
	return edPoint{x, y}
}

func (p edPoint) neg() edPoint {
	x := new(big.Int).Sub(edP, p.x)
	return edPoint{x.Mod(x, edP), p.y}
}

// mul multiplies by the full integer k (k is deliberately not reduced:
// the masks M and N are not in the prime-order subgroup).
func (p edPoint) mul(k *big.Int) edPoint {
	r := edPoint{big.NewInt(0), big.NewInt(1)}
	for i := k.BitLen() - 1; i >= 0; i-- {
		r = r.add(r)
		if k.Bit(i) == 1 {
			r = r.add(p)
		}
	}
	return r
}

// Spake2 is one side of the key exchange.
type Spake2 struct {
	alice           bool
	myName, theirs  []byte
	priv, pwScalar  *big.Int
	pwHash, msgData []byte
}

// NewSpake2 starts the exchange. alice is the client (the computer), bob is
// the device. The names include the trailing NUL, exactly as adb sends them.
func NewSpake2(alice bool, password []byte) (*Spake2, error) {
	s := &Spake2{alice: alice, myName: []byte("adb pair client\x00"), theirs: []byte("adb pair server\x00")}
	if !alice {
		s.myName, s.theirs = s.theirs, s.myName
	}
	rnd := make([]byte, 64)
	if _, err := rand.Read(rnd); err != nil {
		return nil, err
	}
	// Private scalar: reduce, then multiply by the cofactor.
	s.priv = leInt(rnd)
	s.priv.Mod(s.priv, edL).Lsh(s.priv, 3)

	h := sha512.Sum512(password)
	s.pwHash = h[:]
	s.pwScalar = leInt(h[:])
	s.pwScalar.Mod(s.pwScalar, edL)
	// BoringSSL "password scalar hack": add multiples of the group order
	// until the scalar is divisible by eight.
	order := new(big.Int).Set(edL)
	for bit := 0; bit < 3; bit++ {
		if s.pwScalar.Bit(bit) == 1 {
			s.pwScalar.Add(s.pwScalar, order)
		}
		order.Lsh(order, 1)
	}

	mask := spakeN
	if alice {
		mask = spakeM
	}
	s.msgData = edBase.mul(s.priv).add(mask.mul(s.pwScalar)).encode()
	return s, nil
}

// Msg is the 32-byte message to send to the peer.
func (s *Spake2) Msg() []byte { return s.msgData }

// Finish processes the peer's message and returns 64 bytes of key material.
func (s *Spake2) Finish(their []byte) ([]byte, error) {
	q, err := edDecode(their)
	if err != nil {
		return nil, err
	}
	mask := spakeM
	if s.alice {
		mask = spakeN
	}
	dh := q.add(mask.mul(s.pwScalar).neg()).mul(s.priv).encode()

	h := sha512.New()
	put := func(b []byte) {
		var l [8]byte
		binary.LittleEndian.PutUint64(l[:], uint64(len(b)))
		h.Write(l[:])
		h.Write(b)
	}
	if s.alice {
		put(s.myName)
		put(s.theirs)
		put(s.msgData)
		put(their)
	} else {
		put(s.theirs)
		put(s.myName)
		put(their)
		put(s.msgData)
	}
	put(dh)
	put(s.pwHash)
	return h.Sum(nil), nil
}
