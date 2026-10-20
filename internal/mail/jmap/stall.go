package jmap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// ErrStalled means a request or its response stopped moving for longer than
// the client's stall timeout, so it was given up.
var ErrStalled = errors.New("jmap: the server stopped responding mid-transfer")

// DefaultStallTimeout is how long a request may go without sending or
// receiving a byte when Client.StallTimeout is unset.
const DefaultStallTimeout = time.Minute

// do sends req and gives it up once no bytes move either way for the stall
// timeout. Slow transfers that keep moving are never cut off. The returned
// body stays watched until it is closed. Push doesn't use this: its stream
// is quiet between events and has its own watchdog.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	stall := c.StallTimeout
	if stall <= 0 {
		stall = DefaultStallTimeout
	}
	ctx, cancel := context.WithCancelCause(req.Context())
	timer := time.AfterFunc(stall, func() { cancel(ErrStalled) })
	progress := func() { timer.Reset(stall) }
	stop := func() {
		timer.Stop()
		cancel(nil)
	}

	req = req.WithContext(ctx)
	if req.Body != nil {
		req.Body = &progressBody{ReadCloser: req.Body, ctx: ctx, progress: progress}
	}
	if getBody := req.GetBody; getBody != nil {
		// A redirect sends the body again; watch that copy too.
		req.GetBody = func() (io.ReadCloser, error) {
			body, err := getBody()
			if err != nil {
				return nil, err
			}
			return &progressBody{ReadCloser: body, ctx: ctx, progress: progress}, nil
		}
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		err = stalledError(ctx, err)
		stop()
		return nil, err
	}
	progress()
	resp.Body = &progressBody{ReadCloser: resp.Body, ctx: ctx, progress: progress, stop: stop}
	return resp, nil
}

// progressBody resets the stall timer whenever bytes go through it.
type progressBody struct {
	io.ReadCloser
	ctx      context.Context
	progress func()
	stop     func()
}

func (b *progressBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.progress()
	}
	if err != nil && err != io.EOF {
		err = stalledError(b.ctx, err)
	}
	return n, err
}

func (b *progressBody) Close() error {
	err := b.ReadCloser.Close()
	if b.stop != nil {
		b.stop()
	}
	return err
}

// stalledError reports err as ErrStalled when the stall timer ended the
// request.
func stalledError(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrStalled) {
		return ErrStalled
	}
	return err
}
