package nats

import (
	"fmt"
	"net"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// StartEmbeddedServer runs a JetStream-enabled nats-server inside this
// process, listening on every interface at the given port and storing stream
// data under storeDir. The returned server is ready for connections; stop it
// with Shutdown(). Backs the login screen's "Run own NATS server" option
// (port config.EmbeddedPort, storage config.EmbeddedStoreDir).
func StartEmbeddedServer(storeDir string, port int) (*natsserver.Server, error) {
	opts := &natsserver.Options{
		Host:      "0.0.0.0",
		Port:      port,
		JetStream: true,
		StoreDir:  storeDir,
	}
	s, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("embedded nats-server: %w", err)
	}
	s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		s.Shutdown()
		return nil, fmt.Errorf("embedded nats-server not ready (is port %d already in use?)", port)
	}
	return s, nil
}

// LanIP returns the machine's primary IPv4 address on the local network — the
// address other players should dial to reach an embedded server. The UDP dial
// sends nothing; it only resolves which local address routes outward. Falls
// back to scanning the interfaces, then to the loopback address.
func LanIP() string {
	if conn, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		ip, _ := conn.LocalAddr().(*net.UDPAddr)
		conn.Close()
		if ip != nil && ip.IP.To4() != nil && !ip.IP.IsLoopback() {
			return ip.IP.To4().String()
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
				if v4 := ipn.IP.To4(); v4 != nil {
					return v4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}
