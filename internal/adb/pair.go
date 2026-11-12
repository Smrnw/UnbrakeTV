package adb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"
)

// Wireless debugging (Android 11+): pairing by a six-digit code.
// The protocol follows adb's pairing_connection / pairing_auth:
// TLS 1.3 → SPAKE2 over (code + exported TLS key material) → AES-128-GCM
// encrypted exchange of public keys.

const (
	pairVersion     = 1
	pairMsgSpake2   = 0
	pairMsgPeerInfo = 1
	// PeerInfoSize is sizeof(struct PeerInfo) in adb.
	PeerInfoSize = 8192
	// ExportedKeyLabel is used with the TLS exporter; the NUL is part of it.
	ExportedKeyLabel = "adb-label\x00"
	keyName          = "unbrake-tv@pc"
)

// ErrBadPairingCode is returned when the code did not match.
var ErrBadPairingCode = errors.New("код сопряжения не подошёл — проверьте код и порт на экране телевизора")

// TLSCertificate builds the self-signed certificate adb uses for TLS:
// the device recognises the computer by the RSA key inside it.
func TLSCertificate(key *rsa.PrivateKey) (tls.Certificate, error) {
	name := pkix.Name{Country: []string{"US"}, Organization: []string{"Android"}, CommonName: "Adb"}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               name,
		Issuer:                name,
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func clientTLSConfig(key *rsa.PrivateKey) (*tls.Config, error) {
	cert, err := TLSCertificate(key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The TV presents a self-signed certificate, exactly like for adb itself;
		// trust comes from the pairing code, not from a certificate authority.
		InsecureSkipVerify: true,
		// Always offer our certificate: adbd lists key fingerprints as "CAs",
		// which the default selection logic would not match.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &cert, nil },
	}, nil
}

// hkdfSHA256 is HKDF (RFC 5869) with an empty salt, for outputs up to 32 bytes.
func hkdfSHA256(secret, info []byte, n int) []byte {
	ext := hmac.New(sha256.New, make([]byte, sha256.Size))
	ext.Write(secret)
	exp := hmac.New(sha256.New, ext.Sum(nil))
	exp.Write(info)
	exp.Write([]byte{1})
	return exp.Sum(nil)[:n]
}

// PairingCipher encrypts the PeerInfo packets after the SPAKE2 exchange.
type PairingCipher struct {
	aead     cipher.AEAD
	enc, dec uint64
}

// NewPairingCipher derives the AES-128-GCM key from the SPAKE2 key material.
func NewPairingCipher(keyMaterial []byte) (*PairingCipher, error) {
	blk, err := aes.NewCipher(hkdfSHA256(keyMaterial, []byte("adb pairing_auth aes-128-gcm key"), 16))
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &PairingCipher{aead: a}, nil
}

func nonce(seq uint64) []byte {
	n := make([]byte, 12)
	binary.LittleEndian.PutUint64(n, seq)
	return n
}

func (p *PairingCipher) Seal(b []byte) []byte {
	out := p.aead.Seal(nil, nonce(p.enc), b, nil)
	p.enc++
	return out
}

func (p *PairingCipher) Open(b []byte) ([]byte, error) {
	out, err := p.aead.Open(nil, nonce(p.dec), b, nil)
	p.dec++
	return out, err
}

// WritePairingPacket sends one pairing packet (6-byte header + payload).
func WritePairingPacket(w io.Writer, typ byte, payload []byte) error {
	hdr := []byte{pairVersion, typ, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(hdr[2:], uint32(len(payload)))
	_, err := w.Write(append(hdr, payload...))
	return err
}

// ReadPairingPacket reads one pairing packet of the expected type.
func ReadPairingPacket(r io.Reader, typ byte) ([]byte, error) {
	hdr := make([]byte, 6)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[2:])
	if hdr[0] != pairVersion || hdr[1] != typ || n == 0 || n > 2*PeerInfoSize {
		return nil, errors.New("неожиданный ответ при сопряжении")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

// PeerInfo builds the fixed-size structure that carries our public key.
func PeerInfo(pubKey []byte) []byte {
	b := make([]byte, PeerInfoSize)
	// b[0] = 0: ADB_RSA_PUB_KEY
	copy(b[1:], pubKey)
	return b
}

// Pair registers our key on a TV that shows a pairing code
// ("Отладка по Wi-Fi" → "Подключить устройство с помощью кода").
// addr is the pairing address shown under the code.
func Pair(addr, code string, key *rsa.PrivateKey, timeout time.Duration) error {
	code = strings.TrimSpace(code)
	cfg, err := clientTLSConfig(key)
	if err != nil {
		return err
	}
	raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("не удалось подключиться к %s: %w", addr, err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(timeout))
	c := tls.Client(raw, cfg)
	if err := c.Handshake(); err != nil {
		return fmt.Errorf("это не порт сопряжения (%s): %w", addr, err)
	}
	st := c.ConnectionState()
	km, err := st.ExportKeyingMaterial(ExportedKeyLabel, nil, 64)
	if err != nil {
		return err
	}
	sp, err := NewSpake2(true, append([]byte(code), km...))
	if err != nil {
		return err
	}
	if err := WritePairingPacket(c, pairMsgSpake2, sp.Msg()); err != nil {
		return err
	}
	their, err := ReadPairingPacket(c, pairMsgSpake2)
	if err != nil {
		return fmt.Errorf("телевизор прервал сопряжение: %w", err)
	}
	secret, err := sp.Finish(their)
	if err != nil {
		return err
	}
	ciph, err := NewPairingCipher(secret)
	if err != nil {
		return err
	}
	pub, err := AndroidPublicKey(&key.PublicKey, keyName)
	if err != nil {
		return err
	}
	if err := WritePairingPacket(c, pairMsgPeerInfo, ciph.Seal(PeerInfo(pub))); err != nil {
		return err
	}
	// With a wrong code the TV cannot decrypt our packet and drops the
	// connection, or we cannot decrypt its reply.
	reply, err := ReadPairingPacket(c, pairMsgPeerInfo)
	if err != nil {
		return ErrBadPairingCode
	}
	if _, err := ciph.Open(reply); err != nil {
		return ErrBadPairingCode
	}
	return nil
}
