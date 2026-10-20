package jmap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testStall = 100 * time.Millisecond

// within fails the test when fn hasn't returned in a few seconds, so a
// request that hangs shows up as a failure instead of a stuck test.
func within(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the request hung")
		return nil
	}
}

func stallServer(t *testing.T, handler http.HandlerFunc) (*Client, *Session) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	session := &Session{APIURL: server.URL, UploadURL: server.URL + "/upload/{accountId}", DownloadURL: server.URL + "/download/{blobId}"}
	return &Client{HTTP: server.Client(), StallTimeout: testStall}, session
}

// waitGone holds the response open until the client goes away.
func waitGone(w http.ResponseWriter, r *http.Request) {
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

// A reply that stops halfway is given up.
func TestCallGivesUpStalledResponse(t *testing.T) {
	client, session := stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"methodResponses": [`)
		waitGone(w, r)
	})
	err := within(t, func() error {
		_, err := client.Call(context.Background(), session, nil, Invocation{Name: "Core/echo", Args: map[string]any{}, CallID: "0"})
		return err
	})
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("Call = %v", err)
	}
}

// A download is watched until its body is closed.
func TestDownloadBlobGivesUpStalledBody(t *testing.T) {
	client, session := stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "From: a@example.test\r\n")
		waitGone(w, r)
	})
	body, err := client.DownloadBlob(context.Background(), session, "a1", "b1", "", ContentTypeMessage)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	err = within(t, func() error {
		_, err := io.ReadAll(body)
		return err
	})
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("read = %v", err)
	}
}

// An upload the server stops reading is given up.
func TestUploadBlobGivesUpStalledUpload(t *testing.T) {
	// The server doesn't notice a client leaving while the body is unread,
	// so the handler is released by hand before the server closes.
	release := make(chan struct{})
	client, session := stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	defer close(release)
	data := bytes.Repeat([]byte("x"), 64<<20)
	err := within(t, func() error {
		_, err := client.UploadBlob(context.Background(), session, "a1", ContentTypeMessage, data)
		return err
	})
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("UploadBlob = %v", err)
	}
}

// A slow reply that keeps arriving is never cut off, however long it takes.
func TestCallKeepsSlowSteadyResponse(t *testing.T) {
	client, session := stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		reply := `{"methodResponses": [["Core/echo", {}, "0"]], "sessionState": "s1"}`
		for len(reply) > 0 {
			piece := reply[:min(8, len(reply))]
			reply = reply[len(piece):]
			_, _ = io.WriteString(w, piece)
			w.(http.Flusher).Flush()
			time.Sleep(testStall / 2)
		}
	})
	started := time.Now()
	responses, err := client.Call(context.Background(), session, nil, Invocation{Name: "Core/echo", Args: map[string]any{}, CallID: "0"})
	if err != nil || responses.SessionState != "s1" {
		t.Fatalf("Call = %+v, %v", responses, err)
	}
	if time.Since(started) < 2*testStall {
		t.Fatal("the reply wasn't slow enough to test anything")
	}
}
