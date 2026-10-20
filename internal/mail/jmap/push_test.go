package jmap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

// eventServer streams lines from write, flushing after each, then holds the
// connection open until the client leaves or hold is false.
func eventServer(t *testing.T, hold bool, write func(w io.Writer, flush func())) (*httptest.Server, *http.Request) {
	t.Helper()
	var got http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		flusher.Flush()
		write(w, flusher.Flush)
		if hold {
			<-r.Context().Done()
		}
	}))
	t.Cleanup(server.Close)
	return server, &got
}

func TestWatchEventsDeliversStateChanges(t *testing.T) {
	server, req := eventServer(t, false, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, ": a comment\n\nevent: ping\ndata: {\"interval\": 60}\n\n")
		_, _ = io.WriteString(w, "event: state\ndata: {\"@type\":\"StateChange\",\"changed\":{\"a1\":{\"Email\":\"s2\",\"Mailbox\":\"m3\"}}}\n\n")
		// Data split over two lines is one event.
		_, _ = io.WriteString(w, "event: state\ndata: {\"@type\":\"StateChange\",\ndata: \"changed\":{\"a1\":{\"Email\":\"s3\"}}}\n\n")
		flush()
	})
	session := &Session{EventSourceURL: server.URL + "/events?types={types}&closeafter={closeafter}&ping={ping}"}
	opened := false
	var changes []StateChange
	err := (&Client{HTTP: server.Client()}).WatchEvents(context.Background(), session, []string{"Email", "Mailbox"}, time.Minute,
		func() { opened = true }, func(change StateChange) { changes = append(changes, change) })
	if !errors.Is(err, ErrEventStreamEnded) {
		t.Fatalf("err = %v, want the stream to have ended", err)
	}
	if query := req.URL.Query(); query.Get("types") != "Email,Mailbox" || query.Get("closeafter") != "no" || query.Get("ping") != "60" {
		t.Fatalf("query = %v", query)
	}
	if req.Header.Get("Accept") != "text/event-stream" || !opened {
		t.Fatalf("Accept = %q, opened = %v", req.Header.Get("Accept"), opened)
	}
	want := []StateChange{
		{Changed: map[string]map[string]string{"a1": {"Email": "s2", "Mailbox": "m3"}}},
		{Changed: map[string]map[string]string{"a1": {"Email": "s3"}}},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v", changes)
	}
}

// A stream that goes quiet is given up on after two of the server's ping
// intervals, using the interval each ping reports rather than the one asked.
func TestWatchEventsGivesUpOnSilence(t *testing.T) {
	defer func(old time.Duration) { eventSilenceSlack = old }(eventSilenceSlack)
	eventSilenceSlack = 200 * time.Millisecond
	server, _ := eventServer(t, true, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "event: ping\ndata: {\"interval\": 2}\n\n")
		flush()
	})
	session := &Session{EventSourceURL: server.URL + "/events?ping={ping}"}
	start := time.Now()
	err := (&Client{HTTP: server.Client()}).WatchEvents(context.Background(), session, []string{"*"}, time.Second, nil, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrEventStreamSilent) {
		t.Fatalf("err = %v, want silence", err)
	}
	if elapsed < 3*time.Second || elapsed > 8*time.Second {
		t.Fatalf("gave up after %v, want about two 2s intervals", elapsed)
	}
}

func TestWatchEventsStopsWithItsContext(t *testing.T) {
	server, _ := eventServer(t, true, func(io.Writer, func()) {})
	session := &Session{EventSourceURL: server.URL + "/events"}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	err := (&Client{HTTP: server.Client()}).WatchEvents(ctx, session, []string{"*"}, time.Minute,
		func() { once.Do(func() { time.AfterFunc(50*time.Millisecond, cancel) }) }, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's cancellation", err)
	}
}

func TestWatchEventsRefusals(t *testing.T) {
	if err := (&Client{}).WatchEvents(context.Background(), &Session{}, nil, time.Minute, nil, nil); !errors.Is(err, ErrNoPush) {
		t.Fatalf("no eventSourceUrl: err = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sign in", http.StatusUnauthorized)
	}))
	defer server.Close()
	opened := false
	err := (&Client{HTTP: server.Client()}).WatchEvents(context.Background(), &Session{EventSourceURL: server.URL}, nil, time.Minute, func() { opened = true }, nil)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized || opened {
		t.Fatalf("refused stream: err = %v, opened = %v", err, opened)
	}
}

// Watches GOFER_JMAP_TEST_USER's account, changes a keyword on one email and
// waits for the push, then removes the keyword again.
func TestWatchEventsLiveServer(t *testing.T) {
	client, session := liveSession(t)
	account := session.PrimaryAccount(CapabilityMail)
	ids, err := client.QueryEmails(context.Background(), session, EmailQuery{AccountID: account, Limit: 1})
	if err != nil || len(ids.IDs) == 0 {
		t.Fatalf("an email to change: %v, %v", ids, err)
	}
	email := ids.IDs[0]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	opened := make(chan struct{})
	pushed := make(chan StateChange, 4)
	done := make(chan error, 1)
	go func() {
		done <- client.WatchEvents(ctx, session, []string{"Email", "Mailbox"}, time.Minute,
			func() { close(opened) }, func(change StateChange) { pushed <- change })
	}()
	select {
	case <-opened:
	case err := <-done:
		t.Fatalf("WatchEvents: %v", err)
	}
	set := func(on bool) {
		if _, err := client.SetEmails(context.Background(), session, EmailSet{AccountID: account, Update: map[string]PatchObject{email: KeywordPatch("gofer-push-test", on)}}); err != nil {
			t.Fatal(err)
		}
	}
	set(true)
	defer set(false)
	select {
	case change := <-pushed:
		if change.Changed[account]["Email"] == "" {
			t.Fatalf("push without a new Email state for %s: %+v", account, change)
		}
		t.Logf("pushed %+v", change)
	case err := <-done:
		t.Fatalf("WatchEvents ended: %v", err)
	case <-ctx.Done():
		t.Fatal("no push within 20s")
	}
	cancel()
	<-done
}
