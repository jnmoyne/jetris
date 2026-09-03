// Package voice is the in-game voice chat: the microphone's frames encoded
// as IMA ADPCM and published over core NATS (config.VoiceSubject), every
// other player's frames received, buffered against jitter, mixed and played
// back. Everything here but the device files (device_*.go) is pure Go and
// runs unchanged on the desktop and in the browser.
package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The wire format's constants: a frame is 20 ms of 16 kHz mono, 320 samples,
// encoded at four bits a sample — exactly 64 kbit/s of audio, 160 bytes a
// frame — behind an eight-byte header.
const (
	SampleRate   = 16000
	FrameSamples = 320
	FrameBytes   = FrameSamples / 2
	HeaderBytes  = 8
	PacketBytes  = HeaderBytes + FrameBytes

	// VerCodec is the packet's first byte: format version 1 (the high nibble)
	// and codec 1, IMA ADPCM 16 kHz mono in 20 ms frames (the low nibble).
	VerCodec = 0x11

	// FlagStart marks the first packet of a talk-spurt: the receiver's jitter
	// buffer starts afresh from its sequence number rather than treating the
	// gap since the last spurt as loss.
	FlagStart = 0x01
)

// Header is what precedes a frame's nibbles. Every packet carries the codec
// state its frame was encoded from (Predictor and StepIndex, the encoder's
// state BEFORE the frame's first sample), so each one decodes on its own and
// a lost or reordered packet never desynchronises the decoder.
type Header struct {
	VerCodec  uint8
	Flags     uint8
	Seq       uint16 // per sender, wrapping; one counter across talk-spurts and rooms
	Predictor int16
	StepIndex uint8
	Reserved  uint8
}

// Packet is one frame on the wire: the header and the 320 samples as 160
// bytes of nibbles, the earlier sample of each pair in the low nibble.
type Packet struct {
	Header
	Data [FrameBytes]byte
}

// Marshal appends the packet's PacketBytes bytes to dst.
func (p *Packet) Marshal(dst []byte) []byte {
	var h [HeaderBytes]byte
	h[0] = p.VerCodec
	h[1] = p.Flags
	binary.BigEndian.PutUint16(h[2:4], p.Seq)
	binary.BigEndian.PutUint16(h[4:6], uint16(p.Predictor))
	h[6] = p.StepIndex
	h[7] = p.Reserved
	dst = append(dst, h[:]...)
	return append(dst, p.Data[:]...)
}

// ErrBadPacket is what Unmarshal reports for anything that is not a packet
// of this version.
var ErrBadPacket = errors.New("voice: not a voice packet")

// Unmarshal reads a packet back. A wrong length or an unknown version/codec
// byte is ErrBadPacket: a newer client's packets are dropped rather than
// played as noise.
func Unmarshal(b []byte) (Packet, error) {
	var p Packet
	if len(b) != PacketBytes {
		return p, fmt.Errorf("%w: %d bytes, want %d", ErrBadPacket, len(b), PacketBytes)
	}
	if b[0] != VerCodec {
		return p, fmt.Errorf("%w: version/codec byte %#x", ErrBadPacket, b[0])
	}
	p.VerCodec = b[0]
	p.Flags = b[1]
	p.Seq = binary.BigEndian.Uint16(b[2:4])
	p.Predictor = int16(binary.BigEndian.Uint16(b[4:6]))
	p.StepIndex = b[6]
	p.Reserved = b[7]
	if p.StepIndex > maxStepIndex {
		return Packet{}, fmt.Errorf("%w: step index %d", ErrBadPacket, p.StepIndex)
	}
	copy(p.Data[:], b[HeaderBytes:])
	return p, nil
}
