//go:build !js

package webdist

import (
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A fresh directory gets a certificate naming the hosts asked for, localhost
// and the loopbacks; a second load returns the very same one; a certificate
// about to expire, or one that does not name a new host, is remade.
func TestLoadOrCreateCert(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadOrCreateCert(dir, []string{"192.168.1.20", "party.local"})
	if err != nil {
		t.Fatal(err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		t.Fatal("no parsed leaf on the certificate")
	}
	for _, ip := range []string{"192.168.1.20", "127.0.0.1", "::1"} {
		found := false
		for _, s := range leaf.IPAddresses {
			if s.Equal(net.ParseIP(ip)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("certificate does not name %s: %v", ip, leaf.IPAddresses)
		}
	}
	for _, name := range []string{"party.local", "localhost"} {
		found := false
		for _, n := range leaf.DNSNames {
			if n == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("certificate does not name %s: %v", name, leaf.DNSNames)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, keyFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v, mode %v; want 0600", err, info.Mode())
	}

	again, err := LoadOrCreateCert(dir, []string{"192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Leaf.Equal(leaf) {
		t.Fatal("a second load remade the certificate")
	}

	// A host the certificate does not name: remade, naming it too.
	moved, err := LoadOrCreateCert(dir, []string{"10.0.0.7"})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Leaf.Equal(leaf) {
		t.Fatal("a new host did not remake the certificate")
	}
	if !certCovers(moved.Leaf, []string{"10.0.0.7", "localhost"}, time.Now()) {
		t.Fatal("the remade certificate does not name the new host")
	}

	// Expiry: a leaf ending within the renewal window is not good enough.
	stale := &x509.Certificate{NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(certRenewWithin / 2), DNSNames: []string{"localhost"}}
	if certCovers(stale, []string{"localhost"}, time.Now()) {
		t.Fatal("a certificate about to expire was accepted")
	}
}
