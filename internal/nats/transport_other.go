//go:build !js

package nats

import "github.com/nats-io/nats.go"

// transportOptions adapts a server URL to this build's transport. On the
// desktop nats.go dials TCP itself: the URL passes through untouched and no
// options are added.
func transportOptions(url string) (string, []nats.Option) {
	return url, nil
}
