//go:build !js

package nativeui

// embeddedAvailable: the desktop build can host the LAN party mode's
// in-process nats-server, so the login screen offers the tab.
const embeddedAvailable = true
