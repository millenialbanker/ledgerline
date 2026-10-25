// Package bot is the Ledgerline bot: it runs on a staff member's computer,
// opens a browser it controls (Chrome or Edge, through the DevTools
// protocol), records what the person does in it, and replays workflows.
package bot

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ---- a minimal WebSocket client (RFC 6455), for the local browser only ----

type wsConn struct {
	c  net.Conn
	br *bufio.Reader
	wm sync.Mutex
}

func wsDial(ctx context.Context, raw string) (*wsConn, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" {
		return nil, fmt.Errorf("devtools address %q", raw)
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return nil, errors.New("devtools must be on this computer")
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	kb := make([]byte, 16)
	_, _ = rand.Read(kb)
	key := base64.StdEncoding.EncodeToString(kb)
	req := "GET " + u.RequestURI() + " HTTP/1.1\r\nHost: " + u.Host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(c, 1<<16)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		c.Close()
		return nil, err
	}
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(h[:]) {
		c.Close()
		return nil, fmt.Errorf("devtools refused the connection (%s)", resp.Status)
	}
	return &wsConn{c: c, br: br}, nil
}

func (w *wsConn) write(op byte, p []byte) error {
	w.wm.Lock()
	defer w.wm.Unlock()
	hdr := []byte{0x80 | op}
	switch n := len(p); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, b[:]...)
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	hdr = append(hdr, mask[:]...)
	buf := make([]byte, len(hdr)+len(p))
	copy(buf, hdr)
	for i, b := range p {
		buf[len(hdr)+i] = b ^ mask[i%4]
	}
	_, err := w.c.Write(buf)
	return err
}

// read returns the next complete text message.
func (w *wsConn) read() ([]byte, error) {
	var msg []byte
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(w.br, h); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			b := make([]byte, 2)
			if _, err := io.ReadFull(w.br, b); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b))
		case 127:
			b := make([]byte, 8)
			if _, err := io.ReadFull(w.br, b); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b)
		}
		if n > 256<<20 {
			return nil, errors.New("devtools message too large")
		}
		var mask []byte
		if h[1]&0x80 != 0 {
			mask = make([]byte, 4)
			if _, err := io.ReadFull(w.br, mask); err != nil {
				return nil, err
			}
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(w.br, p); err != nil {
			return nil, err
		}
		if mask != nil {
			for i := range p {
				p[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			_ = w.write(0xA, p)
			continue
		case 0xA:
			continue
		case 0x1, 0x2, 0x0:
			msg = append(msg, p...)
			if fin {
				return msg, nil
			}
		}
	}
}

func (w *wsConn) close() { _ = w.write(0x8, nil); w.c.Close() }

// ---- the DevTools protocol ----

// Event is a DevTools event.
type Event struct {
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpResp struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Event
}

// CDP is a connection to a browser.
type CDP struct {
	ws      *wsConn
	mu      sync.Mutex
	next    int64
	pending map[int64]chan cdpResp
	subs    []func(Event)
	done    chan struct{}
	err     error
}

// DialCDP connects to a browser's DevTools address.
func DialCDP(ctx context.Context, wsURL string) (*CDP, error) {
	ws, err := wsDial(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	c := &CDP{ws: ws, pending: map[int64]chan cdpResp{}, done: make(chan struct{})}
	go c.loop()
	return c, nil
}

func (c *CDP) loop() {
	defer close(c.done)
	for {
		b, err := c.ws.read()
		if err != nil {
			c.mu.Lock()
			c.err = err
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = map[int64]chan cdpResp{}
			c.mu.Unlock()
			return
		}
		var r cdpResp
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if r.ID != 0 {
			c.mu.Lock()
			ch := c.pending[r.ID]
			delete(c.pending, r.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- r
			}
			continue
		}
		c.mu.Lock()
		subs := append([]func(Event){}, c.subs...)
		c.mu.Unlock()
		for _, f := range subs {
			f(r.Event)
		}
	}
}

// On calls f for every event (on the reading goroutine: f must not block).
func (c *CDP) On(f func(Event)) {
	c.mu.Lock()
	c.subs = append(c.subs, f)
	c.mu.Unlock()
}

// Done is closed when the browser connection ends.
func (c *CDP) Done() <-chan struct{} { return c.done }

// Call sends a command (to a page session when session is set).
func (c *CDP) Call(ctx context.Context, session, method string, params any, out any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return fmt.Errorf("browser closed: %w", c.err)
	}
	c.next++
	id := c.next
	ch := make(chan cdpResp, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if params == nil {
		params = struct{}{}
	}
	msg := map[string]any{"id": id, "method": method, "params": params}
	if session != "" {
		msg["sessionId"] = session
	}
	b, _ := json.Marshal(msg)
	if err := c.ws.write(0x1, b); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case r, ok := <-ch:
		if !ok {
			return errors.New("the browser was closed")
		}
		if r.Error != nil {
			return fmt.Errorf("%s: %s", method, r.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	}
}

// Close ends the connection.
func (c *CDP) Close() { c.ws.close() }

// timeout is a short context for one command.
func timeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
