package mail

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// EnableOutlookWebhooks makes each successful sync of an Outlook account keep
// the account subscribed to its Inbox changes, announced at notificationURL.
// key signs each subscription's client state. It is called before Start.
func (s *UserIMAP) EnableOutlookWebhooks(notificationURL string, key []byte) {
	s.outlookSubs = newOutlookSubscriptions(notificationURL, key)
}

// keepOutlookSubscription subscribes or renews after the account's receive
// succeeded, through the sync's orchestrator, so the Graph calls share its
// token refresh and the account's provider retry deadline, and deletes the
// subscription of an account set to scheduled checks. Managed mode has no
// subscription loop: background syncs come far more often than renewals.
func (s *UserIMAP) keepOutlookSubscription(ctx context.Context, o *SyncOrchestrator, scope *userIMAPScope, push bool) {
	if s.outlookSubs == nil || scope.config.Provider != providers.ProviderOutlook || scope.tokens == nil {
		return
	}
	if !push {
		s.outlookSubs.drop(ctx, scope.id, userOutlookSubscriptionToken(scope), o.outlookJSON)
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

// OutlookPushStatus reports the account's subscription for the settings page.
// The caller has checked that owner holds the account.
func (s *UserIMAP) OutlookPushStatus(accountID string) OutlookPushStatus {
	return s.outlookSubs.status(accountID, time.Now().UTC())
}

// CheckOutlookPush makes a new subscription for the owner's account now,
// which shows whether Microsoft can reach Gofer.
func (s *UserIMAP) CheckOutlookPush(ctx context.Context, owner, accountID string) error {
	if s.outlookSubs == nil {
		return ErrOutlookPushUnavailable
	}
	return s.operation(ctx, owner, accountID, 0, outlookSubscriptionTimeout, func(ctx context.Context) error {
		scope, err := s.snapshot(ctx, owner, accountID)
		if err != nil {
			return err
		}
		if scope.config.Provider != providers.ProviderOutlook || scope.tokens == nil {
			return errors.New("only Outlook accounts can use change notifications")
		}
		var push bool
		if err := scope.call(ctx, func(db *storage.DB) error {
			push, err = db.ProviderPushEnabled(ctx, accountID)
			return err
		}); err != nil {
			return err
		}
		if !push {
			return errOutlookPushOff
		}
		o := NewSyncOrchestrator(nil, nil, s.blobs, scope.tokens)
		o.imapScope = scope
		return s.outlookSubs.check(ctx, accountID, userOutlookSubscriptionToken(scope), o.outlookJSON, time.Now().UTC())
	})
}
