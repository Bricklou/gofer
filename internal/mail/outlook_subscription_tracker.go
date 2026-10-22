package mail

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/providerquota"
)

// outlookSubscriptions tracks each Outlook account's subscription in memory,
// in either mode: after a restart Gofer deletes the subscriptions it left
// and makes new ones. A nil tracker means GOFER_OUTLOOK_WEBHOOKS is off, and
// its methods do nothing.
type outlookSubscriptions struct {
	url string
	key []byte

	mu       sync.Mutex
	accounts map[string]*outlookSubscriptionState
	// wake is signaled when Graph reports a subscription that needs work
	// before its renewal is due.
	wake chan struct{}
}

type outlookSubscriptionState struct {
	id           string
	expires      time.Time
	renewNow     bool
	cleanedStale bool
	failures     int
	nextAttempt  time.Time
	lastErr      string
	// busy is set while a call to Graph for the account is running, so a
	// check and a renewal cannot both make a subscription.
	busy bool
}

// OutlookPushStatus describes an Outlook account's subscription for the
// settings page.
type OutlookPushStatus struct {
	// Available is false while GOFER_OUTLOOK_WEBHOOKS is off.
	Available bool
	// Subscribed is set while Graph announces the account's Inbox changes.
	Subscribed bool
	Expires    time.Time
	// Error is the last failure to subscribe, kept until one succeeds, and
	// RetryAt is when Gofer tries again on its own.
	Error   string
	RetryAt time.Time
}

// ErrOutlookPushUnavailable refuses a check while GOFER_OUTLOOK_WEBHOOKS is off.
var ErrOutlookPushUnavailable = errors.New("Outlook change notifications are off on this server")

// errOutlookPushOff refuses a check of an account set to scheduled checks.
var errOutlookPushOff = errors.New("this account uses scheduled checks")

// errOutlookSubscriptionBusy reports a subscription call already running for
// the account.
var errOutlookSubscriptionBusy = errors.New("Gofer is already subscribing this account; try again in a moment")

// outlookSubscriptionToken returns a Graph token for the account.
type outlookSubscriptionToken func(context.Context) (string, error)

func newOutlookSubscriptions(notificationURL string, key []byte) *outlookSubscriptions {
	if strings.TrimSpace(notificationURL) == "" || len(key) == 0 {
		return nil
	}
	return &outlookSubscriptions{
		url:      notificationURL,
		key:      append([]byte(nil), key...),
		accounts: make(map[string]*outlookSubscriptionState),
		wake:     make(chan struct{}, 1),
	}
}

// subscribed reports whether Graph currently announces the account's Inbox
// changes, so the quick checks while Gofer is open can leave it alone.
func (t *outlookSubscriptions) subscribed(accountID string, now time.Time) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.accounts[accountID]
	return state != nil && state.id != "" && now.Before(state.expires)
}

func (t *outlookSubscriptions) tracked() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.accounts))
	for accountID := range t.accounts {
		ids = append(ids, accountID)
	}
	return ids
}

// keep subscribes the account when it has no subscription, and renews one
// that ends within a day or that Graph asked to reauthorize. Failures are
// logged and retried after five minutes, doubling up to an hour.
func (t *outlookSubscriptions) keep(ctx context.Context, accountID string, token outlookSubscriptionToken, request graphJSONRequest, now time.Time) {
	if t == nil {
		return
	}
	t.attempt(ctx, accountID, token, request, now, false)
}

// check makes a new subscription for the account right away, replacing any
// it has, and returns why Graph refused it. Only a new subscription shows
// that Microsoft can reach Gofer: Graph calls the notification URL before
// accepting one, and not when renewing.
func (t *outlookSubscriptions) check(ctx context.Context, accountID string, token outlookSubscriptionToken, request graphJSONRequest, now time.Time) error {
	if t == nil {
		return ErrOutlookPushUnavailable
	}
	return t.attempt(ctx, accountID, token, request, now, true)
}

func (t *outlookSubscriptions) attempt(ctx context.Context, accountID string, token outlookSubscriptionToken, request graphJSONRequest, now time.Time, fresh bool) error {
	t.mu.Lock()
	state := t.accounts[accountID]
	if state == nil {
		state = &outlookSubscriptionState{}
		t.accounts[accountID] = state
	}
	due := fresh || !now.Before(state.nextAttempt) && (state.id == "" || state.renewNow || state.expires.Sub(now) < outlookSubscriptionRenewal)
	if !due || state.busy {
		t.mu.Unlock()
		if fresh {
			return errOutlookSubscriptionBusy
		}
		return nil
	}
	state.busy = true
	current := *state
	t.mu.Unlock()

	previous := ""
	if fresh {
		previous, current.id = current.id, ""
	}
	err := t.subscribe(ctx, accountID, &current, token, request, now)
	if err == nil && previous != "" {
		// The new subscription is in place; the old one would announce the
		// same changes again until it ended.
		if err := t.deletePrevious(ctx, token, request, previous); err != nil && ctx.Err() == nil {
			log.Printf("outlook webhook account=%s: delete replaced subscription: %v", accountID, err)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state.busy = false
	if t.accounts[accountID] != state {
		return err
	}
	state.cleanedStale = state.cleanedStale || current.cleanedStale
	if err != nil {
		if ctx.Err() != nil && !fresh {
			return err
		}
		state.failures++
		delay := min(outlookSubscriptionRetry*time.Duration(1<<min(state.failures-1, 4)), outlookSubscriptionMaxRetry)
		state.nextAttempt = now.Add(delay)
		state.lastErr = OutlookPushErrorMessage(err)
		log.Printf("outlook webhook account=%s: %v; trying again in %s", accountID, err, delay)
		return err
	}
	if state.id != current.id {
		log.Printf("outlook webhook account=%s: subscribed until %s", accountID, current.expires.Format(time.RFC3339))
	}
	state.id, state.expires = current.id, current.expires
	state.renewNow, state.failures, state.nextAttempt, state.lastErr = false, 0, time.Time{}, ""
	return nil
}

func (t *outlookSubscriptions) deletePrevious(ctx context.Context, token outlookSubscriptionToken, request graphJSONRequest, id string) error {
	ctx, cancel := context.WithTimeout(ctx, outlookSubscriptionTimeout)
	defer cancel()
	access, err := token(ctx)
	if err != nil {
		return err
	}
	return deleteOutlookSubscription(ctx, request, access, id)
}

// status reports the account's subscription as Gofer last saw it.
func (t *outlookSubscriptions) status(accountID string, now time.Time) OutlookPushStatus {
	if t == nil {
		return OutlookPushStatus{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	status := OutlookPushStatus{Available: true}
	if state := t.accounts[accountID]; state != nil {
		status.Subscribed = state.id != "" && now.Before(state.expires)
		if status.Subscribed {
			status.Expires = state.expires
		}
		status.Error, status.RetryAt = state.lastErr, state.nextAttempt
	}
	return status
}

// subscribe renews the account's subscription, or makes a new one when there
// is none or Graph no longer has it.
func (t *outlookSubscriptions) subscribe(ctx context.Context, accountID string, state *outlookSubscriptionState, token outlookSubscriptionToken, request graphJSONRequest, now time.Time) error {
	ctx, cancel := context.WithTimeout(providerquota.Background(ctx), outlookSubscriptionTimeout)
	defer cancel()
	access, err := token(ctx)
	if err != nil {
		return err
	}
	if state.id != "" {
		expires, err := renewOutlookSubscription(ctx, request, access, state.id, now)
		if status, ok := providerAPIStatus(err); !ok || status != http.StatusNotFound {
			if err == nil {
				state.expires = expires
			}
			return err
		}
		state.id = ""
	}
	if !state.cleanedStale {
		if err := deleteStaleOutlookSubscriptions(ctx, request, access, t.url); err != nil {
			log.Printf("outlook webhook account=%s: remove earlier subscriptions: %v", accountID, err)
		}
		state.cleanedStale = true
	}
	clientState, err := outlookClientState(t.key, accountID)
	if err != nil {
		return err
	}
	created, err := createOutlookSubscription(ctx, request, access, t.url, clientState, now)
	if err != nil {
		return err
	}
	state.id, state.expires = created.ID, created.expires()
	return nil
}

// drop forgets the account's subscription and, given a token, asks Graph to
// delete it while the account's sign-in still works. Otherwise Graph ends it
// on its own within days, and its notifications are ignored meanwhile.
func (t *outlookSubscriptions) drop(ctx context.Context, accountID string, token outlookSubscriptionToken, request graphJSONRequest) {
	if t == nil {
		return
	}
	t.mu.Lock()
	state := t.accounts[accountID]
	delete(t.accounts, accountID)
	t.mu.Unlock()
	if state == nil || state.id == "" || token == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, outlookSubscriptionTimeout)
	defer cancel()
	access, err := token(ctx)
	if err == nil {
		err = deleteOutlookSubscription(ctx, request, access, state.id)
	}
	if err != nil && ctx.Err() == nil {
		log.Printf("outlook webhook account=%s: delete subscription: %v", accountID, err)
	}
}

// receive checks notifications and returns the accounts to sync: those with
// Inbox changes, changes Graph could not deliver, or a subscription Graph
// ended or wants reauthorized, so their sync can also mend the subscription.
// Notifications whose client state Gofer did not sign are ignored.
func (t *outlookSubscriptions) receive(notifications []OutlookNotification) []string {
	if t == nil {
		return nil
	}
	changed := make(map[string]bool)
	wake := false
	t.mu.Lock()
	for _, notification := range notifications {
		accountID, ok := outlookClientStateAccount(t.key, notification.ClientState)
		if !ok {
			continue
		}
		changed[accountID] = true
		state := t.accounts[accountID]
		if state == nil || state.id != notification.SubscriptionID {
			continue
		}
		switch notification.LifecycleEvent {
		case "reauthorizationRequired":
			state.renewNow, state.nextAttempt = true, time.Time{}
			wake = true
		case "subscriptionRemoved":
			state.id, state.nextAttempt = "", time.Time{}
			wake = true
		}
	}
	t.mu.Unlock()
	if wake {
		t.nudge()
	}
	ids := make([]string, 0, len(changed))
	for accountID := range changed {
		ids = append(ids, accountID)
	}
	return ids
}

// nudge wakes the personal-mode subscription loop.
func (t *outlookSubscriptions) nudge() {
	if t == nil {
		return
	}
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// OutlookPushErrorMessage gives Graph's own reason for refusing a
// subscription, such as a failed validation request, without the response
// around it.
func OutlookPushErrorMessage(err error) string {
	var apiErr *providerAPIError
	if errors.As(err, &apiErr) && apiErr != nil {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(apiErr.Body), &body) == nil && strings.TrimSpace(body.Error.Message) != "" {
			return strings.TrimSpace(body.Error.Message)
		}
	}
	return err.Error()
}
