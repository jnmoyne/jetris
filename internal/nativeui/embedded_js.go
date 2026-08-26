//go:build js

package nativeui

// embeddedAvailable: a browser cannot listen for connections, so the LAN
// party mode tab is not offered.
const embeddedAvailable = false
