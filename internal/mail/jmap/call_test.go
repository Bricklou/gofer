package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCallSendsBatchAndSplitsResults(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/jmap/" {
			t.Errorf("request = %s %s, want POST /jmap/", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if user, _, ok := r.BasicAuth(); !ok || user != "robin@example.test" {
			t.Errorf("missing basic auth")
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "methodResponses": [
		    ["Mailbox/get", {"accountId": "a1", "state": "m1", "list": [{"id": "mb1", "name": "Inbox"}]}, "0"],
		    ["error", {"type": "invalidArguments", "description": "limit must be positive"}, "1"]
		  ],
		  "sessionState": "s2"
		}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client(), Auth: BasicAuth("robin@example.test", "secret")}
	session := &Session{APIURL: server.URL + "/jmap/", State: "s1"}
	responses, err := client.Call(context.Background(), session, []string{CapabilityMail},
		Invocation{Name: "Mailbox/get", Args: map[string]any{"accountId": "a1"}, CallID: "0"},
		Invocation{Name: "Email/query", Args: map[string]any{"accountId": "a1", "limit": -1}, CallID: "1"},
	)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	// Core is added to using, and each call goes out as a 3-element array.
	wantBody := map[string]any{
		"using": []any{CapabilityCore, CapabilityMail},
		"methodCalls": []any{
			[]any{"Mailbox/get", map[string]any{"accountId": "a1"}, "0"},
			[]any{"Email/query", map[string]any{"accountId": "a1", "limit": float64(-1)}, "1"},
		},
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Fatalf("request body = %#v\nwant %#v", gotBody, wantBody)
	}

	if responses.SessionState != "s2" {
		t.Fatalf("sessionState = %q, want s2", responses.SessionState)
	}

	var mailboxes struct {
		State string `json:"state"`
		List  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"list"`
	}
	if err := responses.Get("0", &mailboxes); err != nil {
		t.Fatalf("Get(0): %v", err)
	}
	if mailboxes.State != "m1" || len(mailboxes.List) != 1 || mailboxes.List[0].Name != "Inbox" {
		t.Fatalf("mailboxes = %+v", mailboxes)
	}

	// The failed call returns its own error without affecting call 0.
	var methodErr *MethodError
	if err := responses.Get("1", nil); !errors.As(err, &methodErr) || methodErr.Type != "invalidArguments" {
		t.Fatalf("Get(1) err = %v, want MethodError invalidArguments", err)
	}

	if err := responses.Get("9", nil); err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatalf("Get(9) err = %v, want missing response", err)
	}
}

func TestCallRejectsTooManyCallsBeforeSending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called")
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/", Core: CoreCapability{MaxCallsInRequest: 2}}
	calls := []Invocation{
		{Name: "Core/echo", CallID: "0"},
		{Name: "Core/echo", CallID: "1"},
		{Name: "Core/echo", CallID: "2"},
	}
	_, err := client.Call(context.Background(), session, nil, calls...)
	if err == nil || !strings.Contains(err.Error(), "server limit of 2") {
		t.Fatalf("err = %v, want limit error", err)
	}
}

func TestCallReturnsRequestLevelProblem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type": "urn:ietf:params:jmap:error:limit", "limit": "maxSizeRequest", "status": 400}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	_, err := client.Call(context.Background(), session, nil, Invocation{Name: "Core/echo", CallID: "0"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want HTTPError", err)
	}
	if httpErr.Type != "urn:ietf:params:jmap:error:limit" || httpErr.Limit != "maxSizeRequest" {
		t.Fatalf("problem = %+v", httpErr)
	}
}

func TestCallLiveServerEcho(t *testing.T) {
	client, session := liveSession(t)
	responses, err := client.Call(context.Background(), session, nil,
		Invocation{Name: "Core/echo", Args: map[string]any{"hello": "gofer"}, CallID: "0"},
	)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var echoed map[string]any
	if err := responses.Get("0", &echoed); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if echoed["hello"] != "gofer" {
		t.Fatalf("echo = %v", echoed)
	}
	t.Logf("echo=%v sessionState=%s", echoed, responses.SessionState)
}
