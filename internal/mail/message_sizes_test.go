package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func TestOutlookMessageSizesComeFromTheSizeProperty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/$batch" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Requests []providerBatchRequest `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Requests) != 3 {
			t.Errorf("batch = %#v, %v", body, err)
		}
		if !strings.Contains(body.Requests[0].URL, "singleValueExtendedProperties") || !strings.Contains(body.Requests[0].URL, "0x0E08") {
			t.Errorf("request URL = %s", body.Requests[0].URL)
		}
		_, _ = w.Write([]byte(`{"responses":[
			{"id":"0","status":200,"body":{"id":"m-1","singleValueExtendedProperties":[{"id":"Integer 0xe08","value":"52341"}]}},
			{"id":"1","status":404,"body":{"error":{"code":"ErrorItemNotFound"}}},
			{"id":"2","status":200,"body":{"id":"m-3"}}
		]}`))
	}))
	defer server.Close()
	previous := outlookGraphBaseURL
	outlookGraphBaseURL = server.URL
	t.Cleanup(func() { outlookGraphBaseURL = previous })

	sizes := map[string]int64{}
	if err := fetchOutlookMessageSizes(context.Background(), "token", []string{"m-1", "m-2", "m-3"}, sizes); err != nil {
		t.Fatal(err)
	}
	if sizes["m-1"] != 52341 || sizes["m-2"] != storage.MessageSizeUnavailable || sizes["m-3"] != storage.MessageSizeUnavailable {
		t.Fatalf("sizes = %v", sizes)
	}
}

func TestOutlookMessageSizesStopWhenThrottled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"responses":[{"id":"0","status":429,"body":{}}]}`))
	}))
	defer server.Close()
	previous := outlookGraphBaseURL
	outlookGraphBaseURL = server.URL
	t.Cleanup(func() { outlookGraphBaseURL = previous })

	sizes := map[string]int64{}
	if err := fetchOutlookMessageSizes(context.Background(), "token", []string{"m-1"}, sizes); err == nil || len(sizes) != 0 {
		t.Fatalf("throttled lookup = %v, %v", sizes, err)
	}
}

func TestGmailMessageSizeAsksOnlyForTheSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me/messages/g-1" || r.URL.Query().Get("format") != "minimal" || r.URL.Query().Get("fields") != "id,sizeEstimate" {
			t.Errorf("request = %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"id":"g-1","sizeEstimate":81234}`))
	}))
	defer server.Close()
	previous := gmailAPIBaseURL
	gmailAPIBaseURL = server.URL
	t.Cleanup(func() { gmailAPIBaseURL = previous })

	size, err := getGmailAPIMessageSize(context.Background(), "token", "g-1")
	if err != nil || size != 81234 {
		t.Fatalf("size = %d, %v", size, err)
	}
}

func TestGmailSyncKeepsTheSizeEstimate(t *testing.T) {
	syncs := gmailAPIMessageToProviderSyncs("acc", gmailAPIMessage{ID: "g-1", LabelIDs: []string{"INBOX"}, SizeEstimate: 4321}, nil, map[string]gmailAPIFolderSyncTarget{
		"INBOX": {Folder: storage.FolderSyncInfo{ID: "acc_inbox"}, LabelID: "INBOX"},
	})
	if len(syncs) != 1 || syncs[0].SizeBytes != 4321 {
		t.Fatalf("syncs = %#v", syncs)
	}
}
