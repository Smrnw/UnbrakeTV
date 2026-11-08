// Package adb implements a minimal client for the ADB wire protocol over TCP.
// It lets the application talk to an Android TV directly, without adb.exe.
package adb

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	cmdCNXN = 0x4e584e43
	cmdAUTH = 0x48545541
	cmdOPEN = 0x4e45504f
	cmdOKAY = 0x59414b4f
	cmdCLSE = 0x45534c43
	cmdWRTE = 0x45545257
	cmdSTLS = 0x534c5453

	authToken        = 1
	authSignature    = 2
	authRSAPublicKey = 3

	protoVersion = 0x01000000
	maxPayload   = 256 * 1024
)

// ErrAuthRejected is returned when the TV did not accept our key in time.
var ErrAuthRejected = errors.New("телевизор не подтвердил подключение")

// ErrNeedsPairing is returned when the device requires TLS (wireless debugging pairing).
var ErrNeedsPairing = errors.New("устройство требует сопряжения по коду (беспроводная отладка Android 11+), этот режим пока не поддерживается — включите обычную отладку по USB/сети")

type message struct {
	cmd, arg0, arg1 uint32
	data            []byte
}

// Conn is a single ADB connection. All methods are safe for concurrent use;
// commands are executed one at a time.
type Conn struct {
	mu      sync.Mutex
	c       net.Conn
	nextID  uint32
	Banner  string
	maxData uint32
	closed  bool
	addr    string
}

// OnAuthPrompt is called when the TV is showing the "Allow USB debugging?" dialog.
type OnAuthPrompt func()

// Dial connects to addr (host:port) and performs the handshake.
// authWait is how long to wait for the user to confirm the dialog on the TV.
func Dial(addr string, key *rsa.PrivateKey, authWait time.Duration, prompt OnAuthPrompt) (*Conn, error) {
	if !strings.Contains(addr, ":") {
		addr += ":5555"
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к %s: %w", addr, err)
	}
	a := &Conn{c: c, nextID: 1, maxData: maxPayload, addr: addr}
	if err := a.handshake(key, authWait, prompt); err != nil {
		c.Close()
		return nil, err
	}
	return a, nil
}

func (a *Conn) Addr() string { return a.addr }

func (a *Conn) handshake(key *rsa.PrivateKey, authWait time.Duration, prompt OnAuthPrompt) error {
	a.c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := a.send(cmdCNXN, protoVersion, maxPayload, []byte("host::unbraketv\x00")); err != nil {
		return err
	}
	sentSig, sentKey := false, false
	for {
		m, err := a.read()
		if err != nil {
			if sentKey {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					return ErrAuthRejected
				}
			}
			return fmt.Errorf("ошибка рукопожатия ADB: %w", err)
		}
		switch m.cmd {
		case cmdCNXN:
			a.Banner = strings.TrimRight(string(m.data), "\x00")
			if m.arg1 > 0 && m.arg1 < a.maxData {
				a.maxData = m.arg1
			}
			a.c.SetDeadline(time.Time{})
			return nil
		case cmdSTLS:
			return ErrNeedsPairing
		case cmdAUTH:
			if m.arg0 != authToken {
				continue
			}
			if !sentSig {
				sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA1, m.data)
				if err != nil {
					return err
				}
				sentSig = true
				if err := a.send(cmdAUTH, authSignature, 0, sig); err != nil {
					return err
				}
				continue
			}
			if !sentKey {
				pub, err := AndroidPublicKey(&key.PublicKey, "unbrake-tv@pc")
				if err != nil {
					return err
				}
				sentKey = true
				if prompt != nil {
					prompt()
				}
				a.c.SetDeadline(time.Now().Add(authWait))
				if err := a.send(cmdAUTH, authRSAPublicKey, 0, pub); err != nil {
					return err
				}
				continue
			}
			return ErrAuthRejected
		}
	}
}

func checksum(b []byte) uint32 {
	var s uint32
	for _, x := range b {
		s += uint32(x)
	}
	return s
}

func (a *Conn) send(cmd, arg0, arg1 uint32, data []byte) error {
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], cmd)
	binary.LittleEndian.PutUint32(hdr[4:], arg0)
	binary.LittleEndian.PutUint32(hdr[8:], arg1)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(data)))
	binary.LittleEndian.PutUint32(hdr[16:], checksum(data))
	binary.LittleEndian.PutUint32(hdr[20:], cmd^0xffffffff)
	if _, err := a.c.Write(append(hdr, data...)); err != nil {
		return err
	}
	return nil
}

func (a *Conn) read() (*message, error) {
	hdr := make([]byte, 24)
	if _, err := io.ReadFull(a.c, hdr); err != nil {
		return nil, err
	}
	m := &message{
		cmd:  binary.LittleEndian.Uint32(hdr[0:]),
		arg0: binary.LittleEndian.Uint32(hdr[4:]),
		arg1: binary.LittleEndian.Uint32(hdr[8:]),
	}
	n := binary.LittleEndian.Uint32(hdr[12:])
	if binary.LittleEndian.Uint32(hdr[20:]) != m.cmd^0xffffffff {
		return nil, errors.New("повреждённый пакет ADB")
	}
	if n > 16*1024*1024 {
		return nil, errors.New("слишком большой пакет ADB")
	}
	if n > 0 {
		m.data = make([]byte, n)
		if _, err := io.ReadFull(a.c, m.data); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Shell runs a command on the device and returns its combined output.
func (a *Conn) Shell(cmd string, timeout time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", errors.New("соединение закрыто")
	}
	a.c.SetDeadline(time.Now().Add(timeout))
	defer a.c.SetDeadline(time.Time{})

	local := a.nextID
	a.nextID++
	if err := a.send(cmdOPEN, local, 0, []byte("shell:"+cmd+"\x00")); err != nil {
		a.fail()
		return "", err
	}
	var out bytes.Buffer
	var remote uint32
	for {
		m, err := a.read()
		if err != nil {
			a.fail()
			return out.String(), fmt.Errorf("связь с телевизором потеряна: %w", err)
		}
		if m.arg1 != local {
			// Stale message from an older stream: acknowledge writes and move on.
			if m.cmd == cmdWRTE {
				a.send(cmdOKAY, m.arg1, m.arg0, nil)
			}
			continue
		}
		switch m.cmd {
		case cmdOKAY:
			remote = m.arg0
		case cmdWRTE:
			remote = m.arg0
			out.Write(m.data)
			if err := a.send(cmdOKAY, local, remote, nil); err != nil {
				a.fail()
				return out.String(), err
			}
		case cmdCLSE:
			if remote != 0 {
				a.send(cmdCLSE, local, remote, nil)
			}
			if remote == 0 && out.Len() == 0 {
				return "", errors.New("телевизор отказал в выполнении команды")
			}
			return out.String(), nil
		}
	}
}

func (a *Conn) fail() {
	a.closed = true
	a.c.Close()
}

// Alive reports whether the connection is still usable.
func (a *Conn) Alive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.closed
}

// Close closes the connection.
func (a *Conn) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return a.c.Close()
}

// Probe checks that addr really speaks ADB: it sends CNXN and expects a valid
// ADB reply (AUTH, CNXN or STLS). A plain open port (for example a VPN/proxy
// that accepts every connection) is not enough.
func Probe(ctx context.Context, addr string, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout + 1500*time.Millisecond))
	a := &Conn{c: c}
	if err := a.send(cmdCNXN, protoVersion, maxPayload, []byte("host::unbraketv\x00")); err != nil {
		return false
	}
	hdr := make([]byte, 24)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return false
	}
	cmd := binary.LittleEndian.Uint32(hdr[0:])
	if binary.LittleEndian.Uint32(hdr[20:]) != cmd^0xffffffff {
		return false
	}
	return cmd == cmdAUTH || cmd == cmdCNXN || cmd == cmdSTLS
}
