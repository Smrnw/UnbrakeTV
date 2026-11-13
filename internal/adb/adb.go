// Package adb implements a minimal client for the ADB wire protocol over TCP.
// It lets the application talk to an Android TV directly, without adb.exe.
package adb

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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
	tlsVersion   = 0x01000000
	maxPayload   = 256 * 1024
)

// ErrAuthRejected is returned when the TV did not accept our key in time.
var ErrAuthRejected = errors.New("телевизор не подтвердил подключение")

// ErrNeedsPairing is returned when the device requires TLS (wireless debugging pairing).
var ErrNeedsPairing = errors.New("телевизор использует «Отладку по Wi-Fi» и ещё не знает этот компьютер — выполните сопряжение по коду")

type message struct {
	cmd, arg0, arg1 uint32
	data            []byte
}

// Conn is a single ADB connection. All methods are safe for concurrent use;
// commands are executed one at a time.
type Conn struct {
	mu      sync.Mutex
	raw     net.Conn // the TCP socket
	c       net.Conn // raw, or TLS on top of it (wireless debugging)
	nextID  uint32
	Banner  string
	TLS     bool
	maxData uint32
	closed  atomic.Bool
	addr    string
}

// OnAuthPrompt is called when the TV is showing the "Allow USB debugging?" dialog.
type OnAuthPrompt func()

// Dial connects to addr (host:port) and performs the handshake.
// authWait is how long to wait for the user to confirm the dialog on the TV.
func Dial(addr string, key *rsa.PrivateKey, authWait time.Duration, prompt OnAuthPrompt) (*Conn, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "5555")
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к %s: %w", addr, err)
	}
	a := &Conn{raw: c, c: c, nextID: 1, maxData: maxPayload, addr: addr}
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
			if a.TLS {
				// TLS 1.3: the TV rejects an unknown key only after the handshake.
				return ErrNeedsPairing
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
			// Wireless debugging: switch to TLS, the key is checked there.
			if a.TLS {
				return errors.New("ошибка рукопожатия ADB: повторный запрос TLS")
			}
			cfg, err := clientTLSConfig(key)
			if err != nil {
				return err
			}
			if err := a.send(cmdSTLS, tlsVersion, 0, nil); err != nil {
				return err
			}
			tc := tls.Client(a.raw, cfg)
			if err := tc.Handshake(); err != nil {
				return ErrNeedsPairing
			}
			a.c, a.TLS = tc, true
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

// stream is one open ADB service (shell:, sync:). The caller holds a.mu.
type stream struct {
	a             *Conn
	local, remote uint32
	in            bytes.Buffer
	ready, eof    bool
}

var errRefused = errors.New("телевизор отказал в выполнении команды")

func (a *Conn) open(service string) (*stream, error) {
	s := &stream{a: a, local: a.nextID}
	a.nextID++
	if err := a.send(cmdOPEN, s.local, 0, []byte(service+"\x00")); err != nil {
		a.fail()
		return nil, err
	}
	for s.remote == 0 && !s.eof {
		if err := s.pump(); err != nil {
			return nil, err
		}
	}
	if s.remote == 0 {
		return nil, errRefused
	}
	return s, nil
}

// pump reads and handles one message from the device.
func (s *stream) pump() error {
	a := s.a
	m, err := a.read()
	if err != nil {
		a.fail()
		return fmt.Errorf("связь с телевизором потеряна: %w", err)
	}
	if m.arg1 != s.local {
		// Stale message from an older stream: acknowledge writes and move on.
		if m.cmd == cmdWRTE {
			a.send(cmdOKAY, m.arg1, m.arg0, nil)
		}
		return nil
	}
	switch m.cmd {
	case cmdOKAY:
		s.remote, s.ready = m.arg0, true
	case cmdWRTE:
		s.remote = m.arg0
		s.in.Write(m.data)
		if err := a.send(cmdOKAY, s.local, s.remote, nil); err != nil {
			a.fail()
			return err
		}
	case cmdCLSE:
		if s.remote != 0 {
			a.send(cmdCLSE, s.local, s.remote, nil)
		}
		s.eof = true
	}
	return nil
}

// write sends p to the device, waiting for the acknowledgement of every chunk.
func (s *stream) write(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), int(s.a.maxData))
		s.ready = false
		if err := s.a.send(cmdWRTE, s.local, s.remote, p[:n]); err != nil {
			s.a.fail()
			return err
		}
		p = p[n:]
		for !s.ready && !s.eof {
			if err := s.pump(); err != nil {
				return err
			}
		}
		if s.eof {
			return errors.New("телевизор закрыл поток")
		}
	}
	return nil
}

func (s *stream) readN(n int) ([]byte, error) {
	for s.in.Len() < n && !s.eof {
		if err := s.pump(); err != nil {
			return nil, err
		}
	}
	if s.in.Len() < n {
		return nil, io.ErrUnexpectedEOF
	}
	return append([]byte(nil), s.in.Next(n)...), nil
}

// Shell runs a command on the device and returns its combined output.
func (a *Conn) Shell(cmd string, timeout time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return "", errors.New("соединение закрыто")
	}
	a.c.SetDeadline(time.Now().Add(timeout))
	defer a.c.SetDeadline(time.Time{})

	s, err := a.open("shell:" + cmd)
	if err != nil {
		return "", err
	}
	for !s.eof {
		if err := s.pump(); err != nil {
			return s.in.String(), err
		}
	}
	return s.in.String(), nil
}

// Push uploads a file to the device over the sync service.
// progress (optional) receives the number of bytes sent so far.
func (a *Conn) Push(path string, mode uint32, r io.Reader, progress func(sent int64)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return errors.New("соединение закрыто")
	}
	const idle = 60 * time.Second
	a.c.SetDeadline(time.Now().Add(idle))
	defer a.c.SetDeadline(time.Time{})

	s, err := a.open("sync:")
	if err != nil {
		return err
	}
	pkt := func(id string, n uint32, data []byte) []byte {
		b := make([]byte, 8, 8+len(data))
		copy(b, id)
		binary.LittleEndian.PutUint32(b[4:], n)
		return append(b, data...)
	}
	spec := []byte(fmt.Sprintf("%s,%d", path, mode))
	if err := s.write(pkt("SEND", uint32(len(spec)), spec)); err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	var sent int64
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			a.c.SetDeadline(time.Now().Add(idle))
			if err := s.write(pkt("DATA", uint32(n), buf[:n])); err != nil {
				return err
			}
			sent += int64(n)
			if progress != nil {
				progress(sent)
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			// The transfer is half done: the stream cannot be reused.
			a.fail()
			return rerr
		}
	}
	a.c.SetDeadline(time.Now().Add(idle))
	if err := s.write(pkt("DONE", uint32(time.Now().Unix()), nil)); err != nil {
		return err
	}
	resp, err := s.readN(8)
	if err != nil {
		return fmt.Errorf("телевизор не подтвердил запись файла: %w", err)
	}
	if string(resp[:4]) != "OKAY" {
		msg, _ := s.readN(int(binary.LittleEndian.Uint32(resp[4:])))
		return fmt.Errorf("телевизор не принял файл: %s", strings.TrimSpace(string(msg)))
	}
	if err := s.write(pkt("QUIT", 0, nil)); err != nil {
		return nil // the file is already written
	}
	for !s.eof {
		if s.pump() != nil {
			break
		}
	}
	return nil
}

func (a *Conn) fail() {
	a.closed.Store(true)
	a.raw.Close()
}

// Alive reports whether the connection is still usable.
// It never blocks, even while a long command is running.
func (a *Conn) Alive() bool { return !a.closed.Load() }

// Close closes the connection. A command in flight is interrupted.
func (a *Conn) Close() error {
	a.closed.Store(true)
	return a.raw.Close()
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
	a := &Conn{raw: c, c: c}
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
