package mail

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/providerquota"
	"github.com/cristianadrielbraun/gofer/internal/providers"
)

// outlookSubscriptionState is what the shared mode knows about one Outlook
// account's subscription. It lives in memory: after a restart Gofer deletes
// the subscriptions it left and makes new ones.
type outlookSubscriptionState struct {
	id           string
	expires      time.Time
	renewNow     bool
	cleanedStale bool
	failures     int
	nextAttempt  time.Time
}

// EnableOutlookWebhooks makes Start subscribe Outlook accounts to changes,
// announced at notificationURL. key signs each subscription's client state.
func (o *SyncOrchestrator) EnableOutlookWebhooks(notificationURL string, key []byte) {
	if strings.TrimSpace(notificationURL) == "" || len(key) == 0 {
		return
	}
	o.outlookSubsMu.Lock()
	defer o.outlookSubsMu.Unlock()
	o.outlookWebhook = &outlookWebhookConfig{url: notificationURL, key: append([]byte(nil), key...)}
	o.outlookSubs = make(map[string]*outlookSubscriptionState)
	o.outlookSubsWake = make(chan struct{}, 1)
}

func (o *SyncOrchestrator) outlookWebhookConfig() *outlookWebhookConfig {
	o.outlookSubsMu.Lock()
	defer o.outlookSubsMu.Unlock()
	return o.outlookWebhook
}

func (o *SyncOrchestrator) outlookSubscriptionRequest() graphJSONRequest {
	if o.outlookGraphRequest != nil {
		return o.outlookGraphRequest
	}
	return providerJSON
}

func (o *SyncOrchestrator) wakeOutlookSubscriptions() {
	select {
	case o.outlookSubsWake <- struct{}{}:
	default:
	}
}

// outlookSubscribed reports whether Graph currently announces the account's
// Inbox changes, so the quick checks while Gofer is open can leave it alone.
func (o *SyncOrchestrator) outlookSubscribed(accountID string, now time.Time) bool {
	o.outlookSubsMu.Lock()
	defer o.outlookSubsMu.Unlock()
	state := o.outlookSubs[accountID]
	return state != nil && state.id != "" && now.Before(state.expires)
}

func (o *SyncOrchestrator) runOutlookSubscriptions(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(outlookSubscriptionFirstDelay):
	}
	ticker := time.NewTicker(outlookSubscriptionSweep)
	defer ticker.Stop()
	for {
		o.sweepOutlookSubscriptions(ctx, time.Now().UTC())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-o.outlookSubsWake:
		}
	}
}

// sweepOutlookSubscriptions subscribes Outlook accounts that receive mail,
// renews subscriptions nearing their end, and drops those of accounts that
// were removed or stopped receiving mail.
func (o *SyncOrchestrator) sweepOutlookSubscriptions(ctx context.Context, now time.Time) {
	cfg := o.outlookWebhookConfig()
	if cfg == nil {
		return
	}
	ids, err := o.db.GetAllEmailSyncAccountIDs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("outlook webhook: list accounts: %v", err)
		}
		return
	}
	wanted := make(map[string]bool)
	for _, accountID := range ids {
		if provider, err := o.db.GetAccountProvider(ctx, accountID); err == nil && provider == providers.ProviderOutlook {
			wanted[accountID] = true
			o.ensureOutlookSubscription(ctx, cfg, accountID, now)
		}
	}
	o.outlookSubsMu.Lock()
	var dropped []string
	for accountID := range o.outlookSubs {
		if !wanted[accountID] {
			dropped = append(dropped, accountID)
		}
	}
	o.outlookSubsMu.Unlock()
	for _, accountID := range dropped {
		o.dropOutlookSubscription(ctx, accountID)
	}
}

func (o *SyncOrchestrator) ensureOutlookSubscription(ctx context.Context, cfg *outlookWebhookConfig, accountID string, now time.Time) {
	o.outlookSubsMu.Lock()
	state := o.outlookSubs[accountID]
	if state == nil {
		state = &outlookSubscriptionState{}
		o.outlookSubs[accountID] = state
	}
	due := !now.Before(state.nextAttempt) && (state.id == "" || state.renewNow || state.expires.Sub(now) < outlookSubscriptionRenewal)
	current, cleaned := *state, state.cleanedStale
	o.outlookSubsMu.Unlock()
	if !due {
		return
	}

	err := o.subscribeOutlookAccount(ctx, cfg, accountID, &current, cleaned, now)
	o.outlookSubsMu.Lock()
	defer o.outlookSubsMu.Unlock()
	if o.outlookSubs[accountID] != state {
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

// subscribeOutlookAccount renews the account's subscription, or makes a new
// one when there is none or Graph no longer has it.
func (o *SyncOrchestrator) subscribeOutlookAccount(ctx context.Context, cfg *outlookWebhookConfig, accountID string, state *outlookSubscriptionState, cleaned bool, now time.Time) error {
	ctx, cancel := context.WithTimeout(providerquota.Background(ctx), outlookSubscriptionTimeout)
	defer cancel()
	token, err := o.outlookSubscriptionToken(ctx, accountID)
	if err != nil {
		return err
	}
	request := o.outlookSubscriptionRequest()
	if state.id != "" {
		expires, err := renewOutlookSubscription(ctx, request, token, state.id, now)
		if status, ok := providerAPIStatus(err); !ok || status != http.StatusNotFound {
			if err == nil {
				state.expires = expires
			}
			return err
		}
		state.id = ""
	}
	if !cleaned {
		if err := deleteStaleOutlookSubscriptions(ctx, request, token, cfg.url); err != nil {
			log.Printf("outlook webhook account=%s: remove earlier subscriptions: %v", accountID, err)
		}
		state.cleanedStale = true
	}
	clientState, err := outlookClientState(cfg.key, accountID)
	if err != nil {
		return err
	}
	created, err := createOutlookSubscription(ctx, request, token, cfg.url, clientState, now)
	if err != nil {
		return err
	}
	state.id, state.expires = created.ID, created.expires()
	return nil
}

func (o *SyncOrchestrator) outlookSubscriptionToken(ctx context.Context, accountID string) (string, error) {
	graphTokens, ok := o.tokenProvider.(graphMailTokenProvider)
	if !ok {
		return "", errProviderLabelAuth
	}
	return graphTokens.GetMicrosoftGraphMailTokenForAccount(ctx, accountID)
}

// dropOutlookSubscription forgets the subscription of an account that no
// longer receives mail, and asks Graph to delete it while the account's
// sign-in still works. Otherwise Graph ends it on its own within days.
func (o *SyncOrchestrator) dropOutlookSubscription(ctx context.Context, accountID string) {
	o.outlookSubsMu.Lock()
	state := o.outlookSubs[accountID]
	delete(o.outlookSubs, accountID)
	o.outlookSubsMu.Unlock()
	if state == nil || state.id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, outlookSubscriptionTimeout)
	defer cancel()
	token, err := o.outlookSubscriptionToken(ctx, accountID)
	if err == nil {
		err = deleteOutlookSubscription(ctx, o.outlookSubscriptionRequest(), token, state.id)
	}
	if err != nil && ctx.Err() == nil {
		log.Printf("outlook webhook account=%s: delete subscription: %v", accountID, err)
	}
}

// ReceiveOutlookNotifications syncs the accounts whose Inbox changed, and
// passes subscription problems Graph reports to the subscription loop.
// Notifications whose client state Gofer did not sign are ignored.
func (o *SyncOrchestrator) ReceiveOutlookNotifications(notifications []OutlookNotification) {
	cfg := o.outlookWebhookConfig()
	if cfg == nil {
		return
	}
	changed := make(map[string]bool)
	wake := false
	for _, notification := range notifications {
		accountID, ok := outlookClientStateAccount(cfg.key, notification.ClientState)
		if !ok {
			continue
		}
		switch notification.LifecycleEvent {
		case "":
			changed[accountID] = true
		case "missed":
			// Graph could not deliver some changes; a sync finds them.
			changed[accountID] = true
		case "reauthorizationRequired":
			o.outlookSubsMu.Lock()
			if state := o.outlookSubs[accountID]; state != nil && state.id == notification.SubscriptionID {
				state.renewNow, state.nextAttempt = true, time.Time{}
				wake = true
			}
			o.outlookSubsMu.Unlock()
		case "subscriptionRemoved":
			o.outlookSubsMu.Lock()
			if state := o.outlookSubs[accountID]; state != nil && state.id == notification.SubscriptionID {
				state.id, state.nextAttempt = "", time.Time{}
				wake = true
			}
			o.outlookSubsMu.Unlock()
			changed[accountID] = true
		}
	}
	if wake {
		o.wakeOutlookSubscriptions()
	}
	if len(changed) == 0 {
		return
	}
	ctx := o.accountLifecycleContext(context.Background())
	go func() {
		for accountID := range changed {
			if o.db.IsEmailSyncEnabled(ctx, accountID) {
				o.requestPushSync(ctx, accountID)
			}
		}
	}()
}
