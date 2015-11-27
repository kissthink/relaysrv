// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at http://mozilla.org/MPL/2.0/.

package tlsutil

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	mr "math/rand"
	"net"
	"os"
	"time"
)

var (
	ErrIdentificationFailed = fmt.Errorf("failed to identify socket type")
)

// NewCertificate generates and returns a new TLS certificate. If tlsRSABits
// is greater than zero we generate an RSA certificate with the specified
// number of bits. Otherwise we create a 384 bit ECDSA certificate.
func NewCertificate(certFile, keyFile, tlsDefaultCommonName string, tlsRSABits int) (tls.Certificate, error) {
	var priv interface{}
	var err error
	if tlsRSABits > 0 {
		priv, err = rsa.GenerateKey(rand.Reader, tlsRSABits)
	} else {
		priv, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	}
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %s", err)
	}

	notBefore := time.Now()
	notAfter := time.Date(2049, 12, 31, 23, 59, 59, 0, time.UTC)

	template := x509.Certificate{
		SerialNumber: new(big.Int).SetInt64(mr.Int63()),
		Subject: pkix.Name{
			CommonName: tlsDefaultCommonName,
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create cert: %s", err)
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("save cert: %s", err)
	}
	err = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("save cert: %s", err)
	}
	err = certOut.Close()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("save cert: %s", err)
	}

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("save key: %s", err)
	}

	block, err := pemBlockForKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("save key: %s", err)
	}

	err = pem.Encode(keyOut, block)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("save key: %s", err)
	}
	err = keyOut.Close()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("save key: %s", err)
	}

	return tls.LoadX509KeyPair(certFile, keyFile)
}

type DowngradingListener struct {
	net.Listener
	TLSConfig *tls.Config
}

func (l *DowngradingListener) Accept() (net.Conn, error) {
	conn, isTLS, err := l.AcceptNoWrapTLS()

	// We failed to identify the socket type, pretend that everything is fine,
	// and pass it to the underlying handler, and let them deal with it.
	if err == ErrIdentificationFailed {
		return conn, nil
	}

	if err != nil {
		return conn, err
	}

	if isTLS {
		return tls.Server(conn, l.TLSConfig), nil
	}
	return conn, nil
}

func (l *DowngradingListener) AcceptNoWrapTLS() (net.Conn, bool, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, false, err
	}

	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	bs, err := br.Peek(1)
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		// We hit a read error here, but the Accept() call succeeded so we must not return an error.
		// We return the connection as is with a special error which handles this
		// special case in Accept().
		return conn, false, ErrIdentificationFailed
	}

	return &UnionedConnection{br, conn}, bs[0] == 0x16, nil
}

type UnionedConnection struct {
	io.Reader
	net.Conn
}

func (c *UnionedConnection) Read(b []byte) (n int, err error) {
	return c.Reader.Read(b)
}

func publicKey(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func pemBlockForKey(priv interface{}) (*pem.Block, error) {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}, nil
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}, nil
	default:
		return nil, fmt.Errorf("unknown key type")
	}
}
