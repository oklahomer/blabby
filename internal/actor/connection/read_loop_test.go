package connection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// recorder captures messages emitted by a pump and exposes typed channels
// for tests that wait on specific events.
type recorder struct {
	gotClose chan struct{}
	gotData  chan []byte
}

func newRecorder() *recorder {
	return &recorder{
		gotClose: make(chan struct{}, 1),
		gotData:  make(chan []byte, 8),
	}
}

func (r *recorder) notify(msg any) {
	switch m := msg.(type) {
	case *ConnectionClosed:
		select {
		case r.gotClose <- struct{}{}:
		default:
		}
	case *InboundAuth:
		select {
		case r.gotData <- []byte(m.Token.String()):
		default:
		}
	}
}

func dialPaired(t *testing.T) (server *websocket.Conn, client *websocket.Conn, cleanup func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srvConnCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		srvConnCh <- c
	}))
	cli, _, err := websocket.DefaultDialer.Dial("ws://"+strings.TrimPrefix(srv.URL, "http://"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	srvConn := <-srvConnCh
	cleanup = func() {
		_ = cli.Close()
		_ = srvConn.Close()
		srv.Close()
	}
	return srvConn, cli, cleanup
}

func TestRunReadLoop_TextFrameProducesWSInbound(t *testing.T) {
	rec := newRecorder()

	srv, cli, cleanup := dialPaired(t)
	defer cleanup()

	go runReadPump(context.Background(), srv, rec.notify)

	if err := cli.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth","token":"x"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case data := <-rec.gotData:
		if string(data) != "x" {
			t.Errorf("token = %q, want x", data)
		}
	case <-time.After(time.Second):
		t.Fatal("never received *InboundAuth")
	}
}

func TestRunReadLoop_ClientCloseProducesSingleWSClosed(t *testing.T) {
	rec := newRecorder()

	srv, cli, cleanup := dialPaired(t)
	defer cleanup()

	loopExited := make(chan struct{})
	go func() {
		runReadPump(context.Background(), srv, rec.notify)
		close(loopExited)
	}()

	_ = cli.Close()

	select {
	case <-rec.gotClose:
	case <-time.After(time.Second):
		t.Fatal("never received *ConnectionClosed")
	}
	select {
	case <-loopExited:
	case <-time.After(time.Second):
		t.Fatal("loop did not return after close")
	}

	// Drain to verify exactly-one close notification.
	select {
	case <-rec.gotClose:
		t.Fatal("received a second *ConnectionClosed; expected exactly one")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunReadLoop_BinaryFramesAreIgnored(t *testing.T) {
	rec := newRecorder()

	srv, cli, cleanup := dialPaired(t)
	defer cleanup()

	go runReadPump(context.Background(), srv, rec.notify)

	if err := cli.WriteMessage(websocket.BinaryMessage, []byte{0x01, 0x02}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-rec.gotData:
		t.Fatal("binary frame should be ignored, but produced *InboundAuth")
	case <-time.After(150 * time.Millisecond):
		// success: nothing forwarded for binary frames
	}
}

func TestRunReadLoop_OversizedTextFrameCloses(t *testing.T) {
	rec := newRecorder()

	srv, cli, cleanup := dialPaired(t)
	defer cleanup()

	loopExited := make(chan struct{})
	go func() {
		runReadPump(context.Background(), srv, rec.notify)
		close(loopExited)
	}()

	oversized := make([]byte, maxInboundFrameBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	if err := cli.WriteMessage(websocket.TextMessage, oversized); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}

	select {
	case <-rec.gotClose:
	case <-time.After(time.Second):
		t.Fatal("never received *ConnectionClosed for oversized frame")
	}
	select {
	case <-loopExited:
	case <-time.After(time.Second):
		t.Fatal("loop did not return after oversized frame")
	}
}
