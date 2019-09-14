package heos_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/heos"
)

func TestClientContextCancel(t *testing.T) {
	c, ctx, done := testClient(t, nil)
	defer done()

	// Immediately cancel the context and check the result.
	ctx, cancel := context.WithCancel(ctx)
	cancel()

	err := c.System.Heartbeat(ctx)
	if diff := cmp.Diff(context.Canceled.Error(), err.Error()); diff != "" {
		t.Fatalf("unexpected error (-want +got):\n%s", diff)
	}
}

func TestClientContextDeadlineExceeded(t *testing.T) {
	c, ctx, done := testClient(t, nil)
	defer done()

	// Set the shortest possible deadline so that the request must time out.
	ctx, cancel := context.WithTimeout(ctx, 1*time.Nanosecond)
	defer cancel()

	err := c.System.Heartbeat(ctx)
	if diff := cmp.Diff(context.DeadlineExceeded.Error(), err.Error()); diff != "" {
		t.Fatalf("unexpected error (-want +got):\n%s", diff)
	}
}

func TestClientSystemHeartbeat(t *testing.T) {
	c, ctx, done := testClient(t, func(req string) interface{} {
		if diff := cmp.Diff("heos://system/heart_beat\r\n", req); diff != "" {
			panicf("unexpected client request (-want +got):\n%s", diff)
		}

		// Nothing to reply with for heartbeat.
		return nil
	})
	defer done()

	if err := c.System.Heartbeat(ctx); err != nil {
		t.Fatalf("failed to send heartbeat: %v", err)
	}
}

// testClient creates an ephemeral test client and server. The server will
// invoke fn for each client request after the initial heartbeat handshake.
//
// Invoke the cleanup closure to close all connections.
func testClient(t *testing.T, fn func(req string) interface{}) (*heos.Client, context.Context, func()) {
	t.Helper()

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()

		c, err := l.Accept()
		if err != nil {
			panicf("failed to accept: %v", err)
		}
		defer c.Close()

		enc := json.NewEncoder(c)
		b := make([]byte, 128)
		for i := 0; ; i++ {
			n, err := c.Read(b)
			if err != nil {
				// On EOF, terminate this goroutine because the client is
				// closing its connection.
				if err == io.EOF {
					return
				}

				panicf("failed to read request: %v", err)
			}

			// For the first request, always return a canned heartbeat response.
			// Otherwise, invoke the function to return a response.
			if i == 0 {
				// Canned response captured from receiver.
				if _, err := io.WriteString(c, `{"heos": {"command": "system/heart_beat", "result": "success", "message": ""}}`); err != nil {
					panicf("failed to write heartbeat response: %v", err)
				}
			} else {
				if err := enc.Encode(fn(string(b[:n]))); err != nil {
					panicf("failed to encode JSON response: %v", err)
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	// Point the Client at our ephemeral server.
	c, err := heos.Dial(ctx, l.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}

	return c, ctx, func() {
		defer func() {
			cancel()
			wg.Wait()
		}()

		if err := c.Close(); err != nil {
			t.Fatalf("failed to close client: %v", err)
		}

		if err := l.Close(); err != nil {
			t.Fatalf("failed to close listener: %v", err)
		}
	}
}

func panicf(format string, a ...interface{}) {
	panic(fmt.Sprintf(format, a...))
}
