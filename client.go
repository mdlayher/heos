package heos

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"time"
)

// deadlineNow is a time far in the past which can trigger immediate connection
// cancelation.
var deadlineNow = time.Unix(1, 0)

// A Command contains command acknowledgement data returned as a response to
// Client requests.
type Command struct {
	HEOS struct {
		Command string `json:"command"`
		Result  string `json:"result"`
		Message string `json:"message"`
	} `json:"heos"`
}

// A Client is a Denon HEOS protocol client.
type Client struct {
	System System

	mu sync.Mutex
	b  []byte
	c  net.Conn
}

// Dial dials a connection to the device specified by addr. The context is used
// for cancelation and to set timeouts.
func Dial(ctx context.Context, addr string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	c := &Client{
		// TODO(mdlayher): is this enough to read large responses?
		b: make([]byte, os.Getpagesize()),
		c: conn,
	}
	c.System = System{c: c}

	// Perform an initial handshake to verify that the device recognizes the
	// HEOS protocol.
	if err := c.System.Heartbeat(ctx); err != nil {
		return nil, err
	}

	return c, nil
}

// Close closes the Client's connection.
func (c *Client) Close() error {
	return c.c.Close()
}

// Query issues a raw query to a device. The query string should be a HEOS
// request of the form "system/heart_beat" or similar. out is a structure used
// to unmarshal the response JSON data from a query's results.
func (c *Client) Query(ctx context.Context, query string, out interface{}) (*Command, error) {
	u, err := url.Parse(query)
	if err != nil {
		return nil, err
	}
	u.Scheme = "heos"

	// Embed a Command along with the payload to unmarshal the result, so the
	// caller does not have to add Command to their own structures.
	v := struct {
		Command
		Payload interface{} `json:"payload"`
	}{
		Payload: out,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	err = do(ctx, c.c, func(conn net.Conn) error {
		// Commands must have \r\n terminators.
		if _, err := io.WriteString(conn, u.String()+"\r\n"); err != nil {
			return err
		}

		n, err := conn.Read(c.b)
		if err != nil {
			return err
		}

		return json.Unmarshal(c.b[:n], &v)
	})
	if err != nil {
		return nil, err
	}

	// TODO(mdlayher): inspect Command for errors returned by the device.
	return &v.Command, nil
}

// System wraps HEOS System commands.
type System struct {
	c *Client
}

// Heartbeat issues a heartbeat request to a device.
func (s *System) Heartbeat(ctx context.Context) error {
	_, err := s.c.Query(ctx, "system/heart_beat", nil)
	return err
}

// TODO(mdlayher): break this out into netctx package?

// do accepts an input context and net.Conn and invokes fn with the context's
// cancelation and deadline attached to the net.Conn's lifecycle.
func do(ctx context.Context, c net.Conn, fn func(c net.Conn) error) error {
	// Enable immediate connection cancelation via context by using the context's
	// deadline and also setting a deadline in the past if/when the context is
	// canceled. This pattern courtesy of @acln from #networking on Gophers Slack.
	dl, _ := ctx.Deadline()
	if err := c.SetDeadline(dl); err != nil {
		return err
	}

	errC := make(chan error)
	go func() { errC <- fn(c) }()

	select {
	case <-ctx.Done():
		if ctx.Err() == context.Canceled {
			if err := c.SetDeadline(deadlineNow); err != nil {
				return err
			}
		}

		<-errC
		return ctx.Err()
	case err := <-errC:
		return err
	}
}
