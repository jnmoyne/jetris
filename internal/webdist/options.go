package webdist

// Options is how Serve is asked to serve the LAN party page.
//
// CertDir names the directory the page's self-signed certificate is kept in
// (LoadOrCreateCert); with it the page is https, which is what a browser
// wants before it hands a page its microphone or an AudioWorklet — the
// voice chat's needs — and a guest device accepts the certificate once.
// Without it the page is plain http, as a LAN party without a config
// directory would have it: playable, voice listen-only.
//
// Hosts are the names and addresses the page is advertised on, for the
// certificate to name. WSBackend is the embedded nats-server's WebSocket
// listener ("127.0.0.1:4223"): with it every WebSocket upgrade the page's
// port receives, whatever the path, is proxied there, so the browser build
// dials the page's own origin (wss://<host>:<port>) and the one certificate
// the guest accepted for the page covers the socket too — a wss:// to
// another port would be refused without a word, browsers never prompting
// for a socket's certificate.
type Options struct {
	CertDir   string
	Hosts     []string
	WSBackend string
}
