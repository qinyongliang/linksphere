package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/protocol"
)

type initialResizeStream struct {
	mu      sync.Mutex
	data    bytes.Buffer
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	start   sync.Once
	stop    sync.Once
}

func (s *initialResizeStream) Write(data []byte) (int, error) {
	s.start.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-s.closed:
		return 0, io.ErrClosedPipe
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Write(data)
}

func (s *initialResizeStream) Close() error {
	s.stop.Do(func() { close(s.closed) })
	return nil
}

func TestTerminalSessionInitialResizePrecedesInputPublication(t *testing.T) {
	stream := &initialResizeStream{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	defer stream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &terminalSession{ctx: ctx, cancel: cancel, cols: 120, rows: 32, screen: newTerminalScreenBuffer(32)}
	initialized := make(chan struct{})
	go func() { session.setAgentInput(stream); close(initialized) }()
	select {
	case <-stream.entered:
	case <-time.After(time.Second):
		t.Fatal("initial resize did not begin")
	}
	session.mu.Lock()
	ready := session.input != nil
	session.mu.Unlock()
	if ready {
		t.Error("session input became available before its initial resize was queued")
	}
	resized := make(chan struct{})
	go func() { session.resizeTo(111, 37); close(resized) }()
	close(stream.release)
	for _, done := range []<-chan struct{}{initialized, resized} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("initialization or following resize did not settle")
		}
	}
	for index, want := range [][2]uint32{{120, 32}, {111, 37}} {
		frame, err := protocol.ReadFrame(&stream.data)
		if err != nil || frame.Type != protocol.FrameResize || len(frame.Data) != 8 {
			t.Fatalf("resize frame %d: %+v, %v", index, frame, err)
		}
		if got := [2]uint32{binary.BigEndian.Uint32(frame.Data[:4]), binary.BigEndian.Uint32(frame.Data[4:])}; got != want {
			t.Fatalf("resize frame %d=%v, want %v", index, got, want)
		}
	}
	session.mu.Lock()
	ready = session.input != nil
	session.mu.Unlock()
	if !ready {
		t.Fatal("initial resize completed without publishing input")
	}
}

func TestTerminalSessionCloseUnblocksInitialResize(t *testing.T) {
	stream := &initialResizeStream{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	defer stream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &terminalSession{ctx: ctx, cancel: cancel, cols: 120, rows: 32}
	initialized := make(chan struct{})
	go func() { session.setAgentInput(stream); close(initialized) }()
	select {
	case <-stream.entered:
	case <-time.After(time.Second):
		t.Fatal("initial resize did not begin")
	}
	closed := make(chan struct{})
	go func() { session.close(""); close(closed) }()
	for _, done := range []<-chan struct{}{closed, initialized} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("close could not interrupt the blocked initial resize")
		}
	}
	select {
	case <-session.ctx.Done():
	default:
		t.Fatal("closing during initialization did not cancel the session")
	}
	session.mu.Lock()
	ready := session.input != nil
	session.mu.Unlock()
	if ready {
		t.Fatal("closed session republished input after initial resize returned")
	}
}
