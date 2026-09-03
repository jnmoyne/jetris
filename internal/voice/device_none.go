//go:build !js && !cgo

package voice

// A desktop built without cgo has no miniaudio and so no audio at all: the
// Windows arm64 release, or any `CGO_ENABLED=0 go build`. Open fails with
// ErrUnavailable, which the Session records for the mic button to show;
// the rest of the voice machinery (subscriptions, who-is-talking marks)
// works without a device.

// NewDevice is the audio of a build that has none.
func NewDevice() Device {
	return &noDevice{}
}

type noDevice struct {
	io DeviceIO
}

// Open keeps io for SetCapture's reports and fails.
func (d *noDevice) Open(io DeviceIO) error {
	d.io = io
	return ErrUnavailable
}

// SetCapture reports the one outcome there is: no microphone, whatever was
// asked.
func (d *noDevice) SetCapture(bool) {
	if d.io.CaptureState != nil {
		d.io.CaptureState(false, ErrUnavailable)
	}
}

func (*noDevice) Close() {}
