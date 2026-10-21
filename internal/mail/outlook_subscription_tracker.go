package mail

import (
	"context"
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
}

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
	t.mu.Lock()
	state := t.accounts[accountID]
	if state == nil {
		state = &outlookSubscriptionState{}
		t.accounts[accountID] = state
	}
	due := !now.Before(state.nextAttempt) && (state.id == "" || state.renewNow || state.expires.Sub(now) < outlookSubscriptionRenewal)
	current := *state
	t.mu.Unlock()
	if !due {
		return
	}

	err := t.subscribe(ctx, accountID, &current, token, request, now)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.accounts[accountID] != state {
		return
	}
	state.cleanedStale = state.cleanedStale || current.cleanedStale
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		state.failures++
		delay := min(outlookSubscriptionRetry*time.Duration(1<<min(state.failures-1, 4)), outlookSubscriptionMaxRetry)
		state.nextAttempt = now.Add(delay)
		log.Printf("outlook webhook account=%s: %v; trying again in %s", accountID, err, delay)
		return
	}
	if state.id != current.id {
		log.Printf("outlook webhook account=%s: subscribed until %s", accountID, current.expires.Format(time.RFC3339))
	}
	state.id, state.expires = current.id, current.expires
	state.renewNow, state.failures, state.nextAttempt = false, 0, time.Time{}
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
		select {
		case t.wake <- struct{}{}:
		default:
		}
	}
	ids := make([]string, 0, len(changed))
	for accountID := range changed {
		ids = append(ids, accountID)
	}
	return ids
}
