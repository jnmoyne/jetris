//go:build js

package nats

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall/js"
	"time"
)

// wsConn presents a browser WebSocket as a net.Conn carrying a raw byte
// stream: every binary message received is queued as a chunk for Read, and
// every Write is sent as one binary message. Message boundaries are not
// meaningful to nats.go, which parses a stream.
type wsConn struct {
	ws js.Value

	mu       sync.Mutex
	chunks   [][]byte // received, not yet consumed
	err      error    // set once the socket closes or errors
	notify   chan struct{}
	closed   chan struct{}
	once     sync.Once
	deadline time.Time // read deadline (zero = none)

	funcs         []js.Func // event handlers to release on Close
	local, remote wsAddr
}

type wsAddr string

func (a wsAddr) Network() string { return "websocket" }
func (a wsAddr) String() string  { return string(a) }

// dialWebSocket opens url and waits (up to timeout) for the socket to be
// open. Errors from the browser are opaque ("WebSocket error"), so the
// message names the URL, which is what a player needs to see.
func dialWebSocket(url string, timeout time.Duration) (net.Conn, error) {
	wsCtor := js.Global().Get("WebSocket")
	if !wsCtor.Truthy() {
		return nil, errors.New("this browser has no WebSocket support")
	}
	var ws js.Value
	if err := jsTry(func() { ws = wsCtor.New(url) }); err != nil {
		return nil, fmt.Errorf("open %s: %v", url, err)
	}
	ws.Set("binaryType", "arraybuffer")

	c := &wsConn{
		ws:     ws,
		notify: make(chan struct{}, 1),
		closed: make(chan struct{}),
		local:  "browser",
		remote: wsAddr(url),
	}
	opened := make(chan struct{}, 1)

	onOpen := js.FuncOf(func(this js.Value, args []js.Value) any {
		select {
		case opened <- struct{}{}:
		default:
		}
		return nil
	})
	onMessage := js.FuncOf(func(this js.Value, args []js.Value) any {
		data := args[0].Get("data")
		u8 := js.Global().Get("Uint8Array").New(data)
		b := make([]byte, u8.Get("byteLength").Int())
		js.CopyBytesToGo(b, u8)
		c.mu.Lock()
		c.chunks = append(c.chunks, b)
		c.mu.Unlock()
		c.wake()
		return nil
	})
	onClose := js.FuncOf(func(this js.Value, args []js.Value) any {
		reason := ""
		if len(args) > 0 {
			if r := args[0].Get("reason"); r.Truthy() {
				reason = ": " + r.String()
			}
		}
		c.fail(fmt.Errorf("websocket %s closed%s", url, reason))
		return nil
	})
	onError := js.FuncOf(func(this js.Value, args []js.Value) any {
		c.fail(fmt.Errorf("websocket %s: connection error", url))
		return nil
	})
	c.funcs = []js.Func{onOpen, onMessage, onClose, onError}
	ws.Set("onopen", onOpen)
	ws.Set("onmessage", onMessage)
	ws.Set("onclose", onClose)
	ws.Set("onerror", onError)

	select {
	case <-opened:
		return c, nil
	case <-c.closed:
		err := c.err
		c.Close()
		return nil, err
	case <-time.After(timeout):
		c.Close()
		return nil, fmt.Errorf("open %s: timeout after %v", url, timeout)
	}
}

// wake nudges a blocked Read (coalescing: one pending wake is enough).
func (c *wsConn) wake() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// fail records the terminal error and unblocks readers; first error wins.
func (c *wsConn) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.closed)
	})
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.chunks) > 0 {
			n := copy(p, c.chunks[0])
			if n == len(c.chunks[0]) {
				c.chunks = c.chunks[1:]
			} else {
				c.chunks[0] = c.chunks[0][n:]
			}
			c.mu.Unlock()
			return n, nil
		}
		err := c.err
		deadline := c.deadline
		c.mu.Unlock()
		if err != nil {
			return 0, err
		}

		var timer <-chan time.Time
		if !deadline.IsZero() {
			d := time.Until(deadline)
			if d <= 0 {
				return 0, errTimeout
			}
			t := time.NewTimer(d)
			defer t.Stop()
			timer = t.C
		}
		select {
		case <-c.notify:
		case <-c.closed:
		case <-timer:
			return 0, errTimeout
		}
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, c.err
	default:
	}
	if c.ws.Get("readyState").Int() != 1 { // 1 = OPEN
		return 0, io.ErrClosedPipe
	}
	u8 := js.Global().Get("Uint8Array").New(len(p))
	js.CopyBytesToJS(u8, p)
	if err := jsTry(func() { c.ws.Call("send", u8) }); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error {
	c.fail(io.EOF)
	_ = jsTry(func() { c.ws.Call("close") })
	for _, f := range c.funcs {
		f.Release()
	}
	c.funcs = nil
	return nil
}

func (c *wsConn) LocalAddr() net.Addr  { return c.local }
func (c *wsConn) RemoteAddr() net.Addr { return c.remote }

func (c *wsConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }
func (c *wsConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	c.wake() // re-evaluate a blocked Read against the new deadline
	return nil
}

// SetWriteDeadline is a no-op: WebSocket sends are buffered by the browser
// and never block.
func (c *wsConn) SetWriteDeadline(t time.Time) error { return nil }

// errTimeout satisfies net.Error so nats.go treats it like a socket timeout.
var errTimeout net.Error = &timeoutError{}

type timeoutError struct{}

func (*timeoutError) Error() string   { return "i/o timeout" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return true }

// jsTry runs f, converting a thrown JavaScript exception into an error.
func jsTry(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(js.Error); ok {
				err = errors.New(e.Get("message").String())
				return
			}
			err = fmt.Errorf("%v", r)
		}
	}()
	f()
	return nil
}
