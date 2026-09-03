//go:build !js

package webdist

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The LAN party page's certificate.
//
// A browser gives a page its microphone — and an AudioWorklet, which the
// voice chat plays through — only in a secure context: https, or
// localhost. A phone opening http://192.168.1.20:8080/ has neither, so the
// page is served over https, and since nobody on a LAN has a certificate
// a browser trusts, Jetris makes its own: self-signed, kept beside the
// other preferences so it is the same certificate at the next party, and
// every guest device accepts it once (Safari: Show Details → visit this
// website; Chrome: Advanced → proceed). It is remade only when it is about
// to expire or the host has an address it does not name — each remake
// costs every guest that one warning again.

const (
	certFile = "lan-cert.pem"
	keyFile  = "lan-key.pem"
	// certLife is Apple's ceiling for a server certificate's validity;
	// certRenewWithin is how close to the end it is remade.
	certLife        = 825 * 24 * time.Hour
	certRenewWithin = 30 * 24 * time.Hour
)

// LoadOrCreateCert is the certificate for hosts (names or IPs the page is
// advertised on), read from dir or made and written there. The certificate
// always also names localhost, the loopback addresses, and every unicast
// address of the machine's interfaces at the time it is made.
func LoadOrCreateCert(dir string, hosts []string) (tls.Certificate, error) {
	want := certHosts(hosts)
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil && certCovers(leaf, want, time.Now()) {
			cert.Leaf = leaf
			return cert, nil
		}
	}
	return newCert(dir, certPath, keyPath, want)
}

// certCovers reports whether leaf names every host and is good for a while
// yet.
func certCovers(leaf *x509.Certificate, hosts []string, now time.Time) bool {
	if now.Before(leaf.NotBefore) || now.Add(certRenewWithin).After(leaf.NotAfter) {
		return false
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			if !slices.ContainsFunc(leaf.IPAddresses, ip.Equal) {
				return false
			}
		} else if !slices.Contains(leaf.DNSNames, h) {
			return false
		}
	}
	return true
}

// certHosts is the full list a certificate must name: the hosts given,
// localhost and the loopbacks, and the machine's own addresses — a phone
// may reach the page by any of them.
func certHosts(hosts []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, h := range hosts {
		add(h)
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
				add(ipn.IP.String())
			}
		}
	}
	return out
}

// newCert makes a self-signed certificate for hosts and writes the pair.
func newCert(dir, certPath, keyPath string, hosts []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certificate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certificate serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Jetris LAN party", Organization: []string{"Jetris"}},
		NotBefore:             now.Add(-time.Hour), // a guest's clock a little behind
		NotAfter:              now.Add(certLife),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certificate key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert.Leaf, _ = x509.ParseCertificate(der)
	return cert, nil
}
