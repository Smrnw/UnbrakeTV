package adb

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
)

// LoadOrCreateKey reads the RSA key from path or generates a new 2048-bit key.
// Keeping the same key means the TV only asks for confirmation once.
func LoadOrCreateKey(path string) (*rsa.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
				return k, nil
			}
			if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
				if rk, ok := k.(*rsa.PrivateKey); ok {
					return rk, nil
				}
			}
		}
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
		_ = os.WriteFile(path, pemBytes, 0o600)
	}
	return k, nil
}

// AndroidPublicKey encodes the key in the format adbd expects
// (struct RSAPublicKey from mincrypt, base64, followed by " user@host\0").
func AndroidPublicKey(pub *rsa.PublicKey, name string) ([]byte, error) {
	const words = 64 // 2048 bits
	if pub.N.BitLen() != 2048 {
		return nil, errors.New("нужен RSA-ключ 2048 бит")
	}
	buf := make([]byte, 4+4+words*4+words*4+4)
	binary.LittleEndian.PutUint32(buf[0:], words)

	r32 := new(big.Int).Lsh(big.NewInt(1), 32)
	n0 := new(big.Int).Mod(pub.N, r32)
	n0inv := new(big.Int).ModInverse(n0, r32)
	n0inv.Sub(r32, n0inv) // -1/n[0] mod 2^32
	binary.LittleEndian.PutUint32(buf[4:], uint32(n0inv.Uint64()))

	putWords := func(off int, v *big.Int) {
		be := v.FillBytes(make([]byte, words*4))
		for i := 0; i < words; i++ {
			// least significant word first, each word little-endian
			w := binary.BigEndian.Uint32(be[len(be)-4*(i+1):])
			binary.LittleEndian.PutUint32(buf[off+4*i:], w)
		}
	}
	putWords(8, pub.N)
	rr := new(big.Int).Lsh(big.NewInt(1), 2048*2)
	rr.Mod(rr, pub.N)
	putWords(8+words*4, rr)
	binary.LittleEndian.PutUint32(buf[8+words*8:], uint32(pub.E))

	out := base64.StdEncoding.EncodeToString(buf) + " " + name + "\x00"
	return []byte(out), nil
}
