// Package smtptest is a one-connection SMTP relay for tests: implicit TLS, STARTTLS,
// or (to be refused) neither, with a self-signed certificate for 127.0.0.1.
package smtptest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Mode is how the relay offers TLS.
type Mode int

const (
	StartTLS Mode = iota
	Implicit
	Plain // offers no TLS at all
)

// Relay records what one client did.
type Relay struct {
	// Addr is host:port; Roots trusts the relay's certificate.
	Addr  string
	Roots *x509.CertPool

	mu        sync.Mutex
	clearCmds []string
	auth      string
	from      string
	rcpt      []string
	data      string
	done      chan struct{}
}

// Seen is a snapshot of what the relay received.
type Seen struct {
	// ClearCmds are the commands sent before TLS.
	ClearCmds []string
	Auth      string
	From      string
	Rcpt      []string
	Data      string
}

// Seen returns what the relay received so far.
func (r *Relay) Seen() Seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Seen{ClearCmds: append([]string(nil), r.clearCmds...), Auth: r.auth, From: r.from,
		Rcpt: append([]string(nil), r.rcpt...), Data: r.data}
}

// Wait waits until the client has gone, at most d.
func (r *Relay) Wait(d time.Duration) {
	select {
	case <-r.done:
	case <-time.After(d):
	}
}

// Start serves one SMTP session on 127.0.0.1 until the test ends.
func Start(t testing.TB, mode Mode) *Relay {
	t.Helper()
	cert, roots := certificate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	r := &Relay{Addr: ln.Addr().String(), Roots: roots, done: make(chan struct{})}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	go func() {
		defer close(r.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r.serve(conn, cfg, mode)
	}()
	return r
}

func (r *Relay) serve(conn net.Conn, cfg *tls.Config, mode Mode) {
	secure := mode == Implicit
	if secure {
		conn = tls.Server(conn, cfg)
	}
	rd := bufio.NewReader(conn)
	say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	say("220 relay ready")
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		r.mu.Lock()
		if !secure {
			r.clearCmds = append(r.clearCmds, cmd)
		}
		r.mu.Unlock()
		switch cmd {
		case "EHLO", "HELO":
			if mode == StartTLS && !secure {
				say("250-relay\r\n250-STARTTLS\r\n250 8BITMIME")
			} else {
				say("250-relay\r\n250-AUTH PLAIN\r\n250 8BITMIME")
			}
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(conn, cfg)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, secure, rd = tc, true, bufio.NewReader(tc)
		case "AUTH":
			r.set(func() { r.auth = line })
			say("235 ok")
		case "MAIL":
			r.set(func() { r.from = line })
			say("250 ok")
		case "RCPT":
			r.set(func() { r.rcpt = append(r.rcpt, line) })
			say("250 ok")
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := rd.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			r.set(func() { r.data = b.String() })
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("500 what")
		}
	}
}

func (r *Relay) set(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f()
}

func certificate(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}
