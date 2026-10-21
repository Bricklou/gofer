package mail

import (
	"context"
	"log"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/providers"
)

// EnableOutlookWebhooks makes Start subscribe Outlook accounts to changes,
// announced at notificationURL. key signs each subscription's client state.
// It is called before Start.
func (o *SyncOrchestrator) EnableOutlookWebhooks(notificationURL string, key []byte) {
	o.outlookSubs = newOutlookSubscriptions(notificationURL, key)
}

func (o *SyncOrchestrator) outlookSubscriptionRequest() graphJSONRequest {
	if o.outlookGraphRequest != nil {
		return o.outlookGraphRequest
	}
	return providerJSON
}

func (o *SyncOrchestrator) outlookSubscriptionToken(accountID string) outlookSubscriptionToken {
	return func(ctx context.Context) (string, error) {
		graphTokens, ok := o.tokenProvider.(graphMailTokenProvider)
		if !ok {
			return "", errProviderLabelAuth
		}
		return graphTokens.GetMicrosoftGraphMailTokenForAccount(ctx, accountID)
	}
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
		case <-o.outlookSubs.wake:
		}
	}
}

// sweepOutlookSubscriptions subscribes Outlook accounts that receive mail,
// renews subscriptions nearing their end, and drops those of accounts that
// were removed or stopped receiving mail.
func (o *SyncOrchestrator) sweepOutlookSubscriptions(ctx context.Context, now time.Time) {
	if o.outlookSubs == nil {
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
			o.outlookSubs.keep(ctx, accountID, o.outlookSubscriptionToken(accountID), o.outlookSubscriptionRequest(), now)
		}
	}
	for _, accountID := range o.outlookSubs.tracked() {
		if !wanted[accountID] {
			o.outlookSubs.drop(ctx, accountID, o.outlookSubscriptionToken(accountID), o.outlookSubscriptionRequest())
		}
	}
}

// ReceiveOutlookNotifications syncs the accounts Graph announced changes
// for. Subscription problems it reports wake the subscription loop.
func (o *SyncOrchestrator) ReceiveOutlookNotifications(notifications []OutlookNotification) {
	ids := o.outlookSubs.receive(notifications)
	if len(ids) == 0 {
		return
	}
	ctx := o.accountLifecycleContext(context.Background())
	go func() {
		for _, accountID := range ids {
			if o.db.IsEmailSyncEnabled(ctx, accountID) {
				o.requestPushSync(ctx, accountID)
			}
		}
	}()
}
