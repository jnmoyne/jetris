package nats

import "testing"

func TestBrowserDialable(t *testing.T) {
	for _, tc := range []struct {
		url         string
		http, https bool // dialable from an http:// page / from an https:// page
	}{
		{"ws://172.239.19.14:4223", true, false},
		{"wss://demo.nats.io:8443", true, true},
		{"  WSS://Demo.nats.io:8443 ", true, true}, // scheme is case-insensitive, whitespace ignored
		{"WS://host:4223", true, false},
		{"nats://demo.nats.io:4222", false, false},
		{"tls://demo.nats.io:4222", false, false},
		{"http://host", false, false},
		{"", false, false},
		{"wss", false, false}, // a bare scheme is not a URL
	} {
		if got := browserDialable(false, tc.url); got != tc.http {
			t.Errorf("browserDialable(http page, %q) = %v, want %v", tc.url, got, tc.http)
		}
		if got := browserDialable(true, tc.url); got != tc.https {
			t.Errorf("browserDialable(https page, %q) = %v, want %v", tc.url, got, tc.https)
		}
	}
}
