package jmap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// StateChange is a push notification (RFC 8620 section 7.1): for each
// account, the data types whose state changed and their new state, such as
// {"a1": {"Email": "s42"}}. It carries no data; the client asks for the
// changes since its saved state.
type StateChange struct {
	Changed map[string]map[string]string `json:"changed"`
}

// ErrNoPush means the session offers no event source to push changes over.
var ErrNoPush = errors.New("jmap: the server offers no push")

// ErrEventStreamEnded means the server closed the event source.
var ErrEventStreamEnded = errors.New("jmap: the server closed the push connection")

// ErrEventStreamSilent means the event source went quiet for longer than the
// server's own ping interval allows, so the connection is presumed dead.
var ErrEventStreamSilent = errors.New("jmap: the push connection went silent")

// maxEventBytes bounds one line of the event stream.
const maxEventBytes = 1 << 20

// eventSilenceSlack is how much longer than two ping intervals the stream may
// stay quiet before it counts as dead. Tests shorten it.
var eventSilenceSlack = 10 * time.Second

// WatchEvents opens the session's event source for the given types (or all
// types, with "*") and calls onChange for every state change, until ctx ends
// or the connection fails; it always returns an error saying which. onOpen,
// when set, runs once the server has accepted the connection.
//
// The server is asked to ping every ping interval, and may pick a longer one,
// which each ping reports. When nothing arrives, not even a ping, for two of
// the server's intervals and a little more, the connection is presumed dead.
func (c *Client) WatchEvents(ctx context.Context, session *Session, types []string, ping time.Duration, onOpen func(), onChange func(StateChange)) error {
	if session == nil || session.EventSourceURL == "" {
		return ErrNoPush
	}
	seconds := int(ping / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	endpoint := expandTemplate(session.EventSourceURL, map[string]string{
		"types":      strings.Join(types, ","),
		"closeafter": "no",
		"ping":       strconv.Itoa(seconds),
	})
	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if c.Auth != nil {
		c.Auth(req)
	}

	// The watchdog ends the request when the stream stays silent too long;
	// every line read, a ping included, resets it.
	interval := time.Duration(seconds) * time.Second
	silence := time.AfterFunc(2*interval+eventSilenceSlack, func() { cancel(ErrEventStreamSilent) })
	defer silence.Stop()

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return streamError(streamCtx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return newHTTPError(resp)
	}
	if onOpen != nil {
		onOpen()
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 4096), maxEventBytes)
	var event string
	var data strings.Builder
	for scanner.Scan() {
		silence.Reset(2*interval + eventSilenceSlack)
		line := scanner.Text()
		if line != "" {
			// A field line: "name: value", or a comment starting with ":".
			name, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch name {
			case "event":
				event = value
			case "data":
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
			continue
		}
		// A blank line ends the event.
		switch event {
		case "ping":
			var reported struct {
				Interval int `json:"interval"`
			}
			if json.Unmarshal([]byte(data.String()), &reported) == nil && reported.Interval > 0 {
				interval = time.Duration(reported.Interval) * time.Second
				silence.Reset(2*interval + eventSilenceSlack)
			}
		case "state", "":
			var change struct {
				Type string `json:"@type"`
				StateChange
			}
			if data.Len() > 0 && json.Unmarshal([]byte(data.String()), &change) == nil && change.Type == "StateChange" && onChange != nil {
				onChange(change.StateChange)
			}
		}
		event = ""
		data.Reset()
	}
	if err := scanner.Err(); err != nil {
		return streamError(streamCtx, err)
	}
	if err := context.Cause(streamCtx); err != nil {
		return err
	}
	return ErrEventStreamEnded
}

// streamError reports why the stream stopped: the silence watchdog or the
// caller's context if either ended it, or else the read error.
func streamError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return fmt.Errorf("jmap: push connection: %w", err)
}
