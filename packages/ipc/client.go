package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"code.sli.ke/go/vega/packages/common"
)

// Client talks to the daemon over its Unix socket. It is the one dependency a
// controller (UI, CLI) needs: every method is a thin HTTP call, so the same
// client backs the desktop app and command-line tools.
type Client struct {
	hc     *http.Client
	token  string
	socket string
}

// NewClient dials the given socket for every request. The base host is a
// placeholder — the transport ignores it and always dials the socket.
func NewClient(socketPath, token string) *Client {
	dialer := &net.Dialer{}
	return &Client{
		token:  token,
		socket: socketPath,
		hc: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

const base = "http://uploaderd"

func (c *Client) Enqueue(ctx context.Context, req EnqueueRequest) (common.UploadID, error) {
	var out IDResponse
	if err := c.do(ctx, http.MethodPost, "/v1/uploads", req, &out); err != nil {
		return "", err
	}
	return common.UploadID(out.ID), nil
}

func (c *Client) List(ctx context.Context) ([]UploadView, error) {
	var out []UploadView
	return out, c.do(ctx, http.MethodGet, "/v1/uploads", nil, &out)
}

func (c *Client) Get(ctx context.Context, id common.UploadID) (UploadView, error) {
	var out UploadView
	return out, c.do(ctx, http.MethodGet, "/v1/uploads/"+string(id), nil, &out)
}

func (c *Client) Pause(ctx context.Context, id common.UploadID) error {
	return c.do(ctx, http.MethodPost, "/v1/uploads/"+string(id)+"/pause", nil, nil)
}

func (c *Client) Resume(ctx context.Context, id common.UploadID) error {
	return c.do(ctx, http.MethodPost, "/v1/uploads/"+string(id)+"/resume", nil, nil)
}

func (c *Client) Cancel(ctx context.Context, id common.UploadID) error {
	return c.do(ctx, http.MethodPost, "/v1/uploads/"+string(id)+"/cancel", nil, nil)
}

func (c *Client) Metrics(ctx context.Context) (map[string]int64, error) {
	var out map[string]int64
	return out, c.do(ctx, http.MethodGet, "/v1/metrics", nil, &out)
}

// Events streams engine events until ctx is canceled or the daemon closes the
// connection. The returned channel is closed when the stream ends.
func (c *Client) Events(ctx context.Context) (<-chan common.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusErr(resp)
	}

	out := make(chan common.Event)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue // blank separators and comments
			}
			var ev common.Event
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// do sends one request, encoding body if non-nil and decoding into out if non-nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return statusErr(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// statusErr turns a non-2xx reply into an error, surfacing the daemon's message
// and mapping 404 back to common.ErrNotFound so callers can errors.Is it.
func statusErr(resp *http.Response) error {
	var e errorResponse
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %w", e.Error, common.ErrNotFound)
	}
	if e.Error == "" {
		return fmt.Errorf("ipc: %s", resp.Status)
	}
	return fmt.Errorf("ipc: %s", e.Error)
}
