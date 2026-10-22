package mail

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Outlook subscriptions ask Graph to post to Gofer's webhook when anything in
// the mailbox changes, in any folder. Graph keeps a mail subscription for at
// most a few days, so Gofer renews it well before it ends.
const (
	outlookSubscriptionResource = "me/messages"
	outlookSubscriptionLifetime = 70 * time.Hour
	outlookSubscriptionRenewal  = 24 * time.Hour
	outlookSubscriptionTimeout  = 30 * time.Second
	// Graph checks the webhook before it creates a subscription, so the
	// first attempt waits for the server to be listening.
	outlookSubscriptionFirstDelay = 15 * time.Second
	outlookSubscriptionSweep      = time.Minute
	outlookSubscriptionRetry      = 5 * time.Minute
	outlookSubscriptionMaxRetry   = time.Hour
	outlookClientStateMaxLength   = 128
)

// outlookClientState is the secret Graph repeats in every notification of
// an account's subscription. It names the account and signs that name with
// Gofer's key, so a notification proves which account it is for without
// Gofer storing a secret per subscription.
func outlookClientState(key []byte, accountID string) (string, error) {
	state := accountID + "." + outlookClientStateSignature(key, accountID)
	if accountID == "" || len(state) > outlookClientStateMaxLength {
		return "", fmt.Errorf("account ID %q does not fit an Outlook subscription", accountID)
	}
	return state, nil
}

// outlookClientStateAccount returns the account a notification's client
// state was made for, if Gofer's key signed it.
func outlookClientStateAccount(key []byte, state string) (string, bool) {
	dot := strings.LastIndexByte(state, '.')
	if dot <= 0 || len(state) > outlookClientStateMaxLength {
		return "", false
	}
	accountID := state[:dot]
	want := outlookClientStateSignature(key, accountID)
	if !hmac.Equal([]byte(state[dot+1:]), []byte(want)) {
		return "", false
	}
	return accountID, true
}

func outlookClientStateSignature(key []byte, accountID string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("gofer outlook webhook\x00" + accountID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type outlookGraphSubscription struct {
	ID                 string `json:"id,omitempty"`
	ChangeType         string `json:"changeType,omitempty"`
	Resource           string `json:"resource,omitempty"`
	NotificationURL    string `json:"notificationUrl,omitempty"`
	LifecycleURL       string `json:"lifecycleNotificationUrl,omitempty"`
	ExpirationDateTime string `json:"expirationDateTime,omitempty"`
	ClientState        string `json:"clientState,omitempty"`
}

type outlookGraphSubscriptionsResponse struct {
	Value    []outlookGraphSubscription `json:"value"`
	NextLink string                     `json:"@odata.nextLink"`
}

func (s outlookGraphSubscription) expires() time.Time {
	at, err := time.Parse(time.RFC3339Nano, s.ExpirationDateTime)
	if err != nil {
		return time.Time{}
	}
	return at.UTC()
}

// createOutlookSubscription subscribes to the account's whole mailbox. Graph answers
// only after the webhook repeated its validation token.
func createOutlookSubscription(ctx context.Context, request graphJSONRequest, token, notificationURL, clientState string, now time.Time) (outlookGraphSubscription, error) {
	body := outlookGraphSubscription{
		ChangeType:         "created,updated,deleted",
		Resource:           outlookSubscriptionResource,
		NotificationURL:    notificationURL,
		LifecycleURL:       notificationURL,
		ExpirationDateTime: now.Add(outlookSubscriptionLifetime).UTC().Format(time.RFC3339),
		ClientState:        clientState,
	}
	var created outlookGraphSubscription
	err := request(ctx, http.MethodPost, outlookGraphBaseURL+"/subscriptions", token, nil, body, &created)
	if err == nil && (strings.TrimSpace(created.ID) == "" || created.expires().IsZero()) {
		err = fmt.Errorf("Graph created a subscription without an ID or an end")
	}
	return created, err
}

// renewOutlookSubscription moves a subscription's end out again. Graph also
// takes a renewal as reauthorizing the subscription.
func renewOutlookSubscription(ctx context.Context, request graphJSONRequest, token, id string, now time.Time) (time.Time, error) {
	body := outlookGraphSubscription{ExpirationDateTime: now.Add(outlookSubscriptionLifetime).UTC().Format(time.RFC3339)}
	var renewed outlookGraphSubscription
	if err := request(ctx, http.MethodPatch, outlookGraphBaseURL+"/subscriptions/"+url.PathEscape(id), token, nil, body, &renewed); err != nil {
		return time.Time{}, err
	}
	if renewed.expires().IsZero() {
		return time.Time{}, fmt.Errorf("Graph renewed subscription %s without an end", id)
	}
	return renewed.expires(), nil
}

func deleteOutlookSubscription(ctx context.Context, request graphJSONRequest, token, id string) error {
	err := request(ctx, http.MethodDelete, outlookGraphBaseURL+"/subscriptions/"+url.PathEscape(id), token, nil, nil, nil)
	if status, ok := providerAPIStatus(err); ok && status == http.StatusNotFound {
		return nil
	}
	return err
}

// deleteStaleOutlookSubscriptions removes the account's subscriptions to
// this webhook left from before Gofer restarted, which nothing renews.
func deleteStaleOutlookSubscriptions(ctx context.Context, request graphJSONRequest, token, notificationURL string) error {
	endpoint := outlookGraphBaseURL + "/subscriptions"
	for page := 0; endpoint != "" && page < 10; page++ {
		var response outlookGraphSubscriptionsResponse
		if err := request(ctx, http.MethodGet, endpoint, token, nil, nil, &response); err != nil {
			return err
		}
		for _, subscription := range response.Value {
			if subscription.NotificationURL != notificationURL || strings.TrimSpace(subscription.ID) == "" {
				continue
			}
			if err := deleteOutlookSubscription(ctx, request, token, subscription.ID); err != nil {
				return err
			}
		}
		endpoint = strings.TrimSpace(response.NextLink)
		if endpoint != "" {
			if err := validateUserGraphEndpoint(endpoint); err != nil {
				return err
			}
		}
	}
	return nil
}
