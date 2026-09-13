//go:build !js && !linux && !windows && !(darwin && cgo)

package gamepad

// A build with no controller support at all — a desktop built without cgo
// on macOS, or a platform none of the backends know: the pad is never
// connected and never speaks.

type noSource struct{ queue }

// Open is the platform's Source: here, none.
func Open() Source { return &noSource{} }

func (n *noSource) Start(wake func()) { n.setWake(wake) }
func (n *noSource) Stop()             {}
