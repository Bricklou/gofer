package mail

import (
	"context"
	"log"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/providers"
)

// EnableOutlookWebhooks makes each successful sync of an Outlook account keep
// the account subscribed to its Inbox changes, announced at notificationURL.
// key signs each subscription's client state. It is called before Start.
func (s *UserIMAP) EnableOutlookWebhooks(notificationURL string, key []byte) {
	s.outlookSubs = newOutlookSubscriptions(notificationURL, key)
}

// keepOutlookSubscription subscribes or renews after the account's receive
// succeeded, through the sync's orchestrator, so the Graph calls share its
// token refresh and the account's provider retry deadline. Managed mode has
// no subscription loop: background syncs come far more often than renewals.
func (s *UserIMAP) keepOutlookSubscription(ctx context.Context, o *SyncOrchestrator, scope *userIMAPScope) {
	if s.outlookSubs == nil || scope.config.Provider != providers.ProviderOutlook || scope.tokens == nil {
		return
	}
	s.outlookSubs.keep(ctx, scope.id, userOutlookSubscriptionToken(scope), o.outlookJSON, time.Now().UTC())
}

// dropOutlookSubscription deletes the subscription of an account that
// stopped receiving mail.
func (s *UserIMAP) dropOutlookSubscription(ctx context.Context, scope *userIMAPScope) {
	if s.outlookSubs == nil || scope.tokens == nil {
		return
	}
	o := NewSyncOrchestrator(nil, nil, s.blobs, scope.tokens)
	o.imapScope = scope
	s.outlookSubs.drop(ctx, scope.id, userOutlookSubscriptionToken(scope), o.outlookJSON)
}

func userOutlookSubscriptionToken(scope *userIMAPScope) outlookSubscriptionToken {
	return func(ctx context.Context) (string, error) {
		graphTokens, ok := scope.tokens.(graphMailTokenProvider)
		if !ok {
			return "", errProviderLabelAuth
		}
		return graphTokens.GetMicrosoftGraphMailTokenForAccount(ctx, scope.id)
	}
}

// ReceiveOutlookNotifications queues a sync of each account Graph announced
// changes for. A sync already running is followed by another, since it may
// have read the Inbox before the change.
func (s *UserIMAP) ReceiveOutlookNotifications(notifications []OutlookNotification) {
	ids := s.outlookSubs.receive(notifications)
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	if s.closing || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		owners, err := s.Routing().ActiveAccountOwners(s.ctx, ids)
		if err != nil {
			if s.ctx.Err() == nil {
				log.Printf("outlook webhook: find account owners: %v", err)
			}
			return
		}
		for accountID, owner := range owners {
			// A new polling revision makes a running sync schedule another.
			if err := s.Routing().ResetAccountPolling(s.ctx, owner, accountID); err != nil && s.ctx.Err() == nil {
				log.Printf("outlook webhook account=%s: %v", accountID, err)
			}
		}
		s.wakeBackground()
		for accountID := range owners {
			if err := s.QueueAccount(s.ctx, accountID); err != nil && s.ctx.Err() == nil {
				log.Printf("outlook webhook account=%s: queue sync: %v", accountID, err)
			}
		}
	}()
}
