package mail

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const (
	// Graph sends a few hundred bytes per notification and batches them, so
	// this leaves room for large batches without reading unbounded bodies.
	outlookWebhookMaxBody            = 4 << 20
	outlookWebhookMaxValidationToken = 4 << 10
)

// OutlookNotification is one change or lifecycle notification Graph sent for
// an Outlook subscription. Nothing in it is trusted until ClientState matches
// the secret saved with the subscription.
type OutlookNotification struct {
	SubscriptionID string
	ClientState    string
	// LifecycleEvent is empty for a change, or reauthorizationRequired,
	// subscriptionRemoved or missed.
	LifecycleEvent string
}

// OutlookNotificationReceiver checks notifications and queues what they ask
// for. It must not wait on Graph or a sync: Graph expects an answer within
// three seconds and stops sending to endpoints that keep it waiting.
type OutlookNotificationReceiver interface {
	ReceiveOutlookNotifications([]OutlookNotification)
}

type outlookWebhookBody struct {
	Value []struct {
		SubscriptionID string `json:"subscriptionId"`
		ClientState    string `json:"clientState"`
		LifecycleEvent string `json:"lifecycleEvent"`
	} `json:"value"`
}

// OutlookWebhookHandler answers Graph at httpguard.OutlookWebhookPath, both
// for change notifications and lifecycle notifications. A nil receiver
// acknowledges notifications without acting on them.
func OutlookWebhookHandler(receiver OutlookNotificationReceiver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Graph checks a new subscription's URL by asking it to repeat a token.
		if r.URL.Query().Has("validationToken") {
			token := r.URL.Query().Get("validationToken")
			if token == "" || len(token) > outlookWebhookMaxValidationToken {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, token)
			return
		}
		var body outlookWebhookBody
		if err := json.NewDecoder(io.LimitReader(r.Body, outlookWebhookMaxBody)).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		notifications := make([]OutlookNotification, 0, len(body.Value))
		for _, value := range body.Value {
			id := strings.TrimSpace(value.SubscriptionID)
			if id == "" {
				continue
			}
			notifications = append(notifications, OutlookNotification{
				SubscriptionID: id,
				ClientState:    value.ClientState,
				LifecycleEvent: strings.TrimSpace(value.LifecycleEvent),
			})
		}
		if receiver != nil && len(notifications) > 0 {
			receiver.ReceiveOutlookNotifications(notifications)
		}
		w.WriteHeader(http.StatusAccepted)
	})
}
