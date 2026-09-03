//go:build !js && cgo

package voice

import (
	"os"
	"testing"
	"time"
)

// TestMalgoSpeakers opens the machine's real speakers through miniaudio and
// pulls frames from a render callback for a moment — the one test that
// touches audio hardware, so it runs only when asked (JETRIS_AUDIO_TEST=1):
// a CI runner has no sound device, and a laptop's should not be opened by
// every `go test`. It never opens the microphone (no permission prompt).
func TestMalgoSpeakers(t *testing.T) {
	if os.Getenv("JETRIS_AUDIO_TEST") == "" {
		t.Skip("set JETRIS_AUDIO_TEST=1 to open the real speakers")
	}
	dev := NewDevice()
	rendered := make(chan struct{}, 1)
	err := dev.Open(DeviceIO{
		Render: func(out []int16) {
			select {
			case rendered <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("open speakers: %v", err)
	}
	defer dev.Close()
	select {
	case <-rendered:
	case <-time.After(2 * time.Second):
		t.Fatal("the speakers never asked for a frame")
	}
}
