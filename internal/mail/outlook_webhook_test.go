package mail

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type recordedOutlookNotifications struct {
	batches [][]OutlookNotification
}

func (r *recordedOutlookNotifications) ReceiveOutlookNotifications(notifications []OutlookNotification) {
	r.batches = append(r.batches, notifications)
}

func TestOutlookWebhookRepeatsTheValidationToken(t *testing.T) {
	receiver := &recordedOutlookNotifications{}
	token := "Validation: Token <with> & symbols"
	r := httptest.NewRequest(http.MethodPost, "/webhooks/outlook?validationToken="+url.QueryEscape(token), nil)
	w := httptest.NewRecorder()
	OutlookWebhookHandler(receiver).ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != token {
		t.Fatalf("validation answer = %d %q, want 200 %q", w.Code, w.Body.String(), token)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("Content-Type = %q", got)
	}
	if len(receiver.batches) != 0 {
		t.Fatalf("validation reached the receiver: %#v", receiver.batches)
	}
	for _, query := range []string{"validationToken=", "validationToken=" + strings.Repeat("a", outlookWebhookMaxValidationToken+1)} {
		w := httptest.NewRecorder()
		OutlookWebhookHandler(receiver).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhooks/outlook?"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("token of %d bytes: status = %d", len(query), w.Code)
		}
	}
}

func TestOutlookWebhookHandsNotificationsToTheReceiver(t *testing.T) {
	receiver := &recordedOutlookNotifications{}
	body := `{"value":[
		{"subscriptionId":"sub-1","clientState":"secret-1","changeType":"created","resource":"Users/x/Messages/y"},
		{"subscriptionId":" ","clientState":"ignored"},
		{"subscriptionId":"sub-2","clientState":"secret-2","lifecycleEvent":"reauthorizationRequired"}
	]}`
	w := httptest.NewRecorder()
	OutlookWebhookHandler(receiver).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhooks/outlook", strings.NewReader(body)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	want := [][]OutlookNotification{{
		{SubscriptionID: "sub-1", ClientState: "secret-1"},
		{SubscriptionID: "sub-2", ClientState: "secret-2", LifecycleEvent: "reauthorizationRequired"},
	}}
	if !reflect.DeepEqual(receiver.batches, want) {
		t.Fatalf("received %#v, want %#v", receiver.batches, want)
	}
}

func TestOutlookWebhookRejectsWhatGraphDoesNotSend(t *testing.T) {
	receiver := &recordedOutlookNotifications{}
	for _, tt := range []struct {
		method, body string
		want         int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPost, "not json", http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		OutlookWebhookHandler(receiver).ServeHTTP(w, httptest.NewRequest(tt.method, "/webhooks/outlook", strings.NewReader(tt.body)))
		if w.Code != tt.want {
			t.Fatalf("%s %q status = %d, want %d", tt.method, tt.body, w.Code, tt.want)
		}
	}
	// Without a receiver, notifications are acknowledged and dropped.
	w := httptest.NewRecorder()
	OutlookWebhookHandler(nil).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhooks/outlook", strings.NewReader(`{"value":[{"subscriptionId":"sub-1"}]}`)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("nil receiver status = %d", w.Code)
	}
	if len(receiver.batches) != 0 {
		t.Fatalf("rejected requests reached the receiver: %#v", receiver.batches)
	}
}
