package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/mail/imap"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	goimap "github.com/emersion/go-imap/v2"
)

// messageFlagBatchLimit is how many read or starred changes the worker
// sends to a provider together.
const messageFlagBatchLimit = 500

// messageFlagBatchKey groups claimed changes that can share one request.
func messageFlagBatchKey(m storage.MessageMutation) string {
	return strings.Join([]string{m.AccountID, m.ProviderType, m.Kind, fmt.Sprint(m.TargetValue), m.FolderID}, "\x00")
}

// applyQueuedMessageFlagBatch sends claimed read or starred changes of one
// account, folder and value, with any further due ones, to the provider in
// one request: one IMAP STORE over their UIDs, or Email/set calls naming
// many emails on JMAP. A change whose message must be looked up on the
// server first goes one by one, and one whose message is gone is dropped.
func (h *Handler) applyQueuedMessageFlagBatch(parent context.Context, group []storage.MessageMutation) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	lead := group[0]
	if more, err := h.db.ClaimMessageFlagBatch(ctx, lead, time.Now(), messageFlagBatchLimit-len(group)); err != nil {
		log.Printf("message-mutation: claim %s batch: %v", lead.Kind, err)
	} else {
		group = append(group, more...)
	}

	var grouped []storage.MessageMutation
	var infos []storage.MessageMutationInfo
	for _, m := range group {
		var info *storage.MessageMutationInfo
		var err error
		if m.FolderID == "" {
			info, err = h.db.GetMessageMutationInfoInternal(ctx, m.MessageID)
		} else {
			info, err = h.db.GetMessageMutationInfoInFolder(ctx, m.MessageID, m.FolderID)
		}
		switch {
		case err != nil:
			h.finishQueuedMessageMutation(m, err)
		case info == nil:
			if err := h.db.DiscardMessageMutation(ctx, m.ID); err != nil {
				log.Printf("message-mutation: discard id=%s: %v", m.ID, err)
			}
		case info.AccountID != m.AccountID:
			h.finishQueuedMessageMutation(m, errors.New("message account changed"))
		case messageMutationProvider(info.AccountProvider) != m.ProviderType:
			h.finishQueuedMessageMutation(m, fmt.Errorf("message provider changed from %s", m.ProviderType))
		case m.ProviderType == storage.MessageMutationProviderIMAP && info.RemoteUID == 0,
			m.ProviderType == storage.MessageMutationProviderJMAP && info.RemoteMessageID == "":
			h.applyQueuedMessageMutation(ctx, m)
		case m.ProviderType == storage.MessageMutationProviderIMAP && strings.TrimSpace(info.FolderRemoteID) == "":
			h.finishQueuedMessageMutation(m, errors.New("message has no remote IMAP folder identity"))
		default:
			grouped = append(grouped, m)
			infos = append(infos, *info)
		}
	}
	if len(grouped) == 0 {
		return
	}

	var failures []error
	switch lead.ProviderType {
	case storage.MessageMutationProviderIMAP:
		failures = h.storeIMAPFlagBatch(ctx, lead, infos)
	case storage.MessageMutationProviderJMAP:
		failures = h.setJMAPFlagBatch(ctx, lead, infos)
	default:
		failures = sameMutationError(len(grouped), fmt.Errorf("unsupported batched provider %q", lead.ProviderType))
	}
	for i, m := range grouped {
		h.finishQueuedMessageMutation(m, failures[i])
	}
}

// finishQueuedMessageMutation records a change's result as
// applyQueuedMessageMutation does: applied, or failed with a retry.
func (h *Handler) finishQueuedMessageMutation(m storage.MessageMutation, applyErr error) {
	if applyErr == nil {
		applyErr = h.db.CompleteMessageMutation(context.Background(), m.ID)
		if applyErr == nil {
			return
		}
		applyErr = errors.New("Provider update succeeded, but Gofer could not save the result: " + applyErr.Error())
	}
	nextAttempt := time.Now().Add(sentCopyRetryDelay(m.AttemptCount))
	if err := h.db.FinishMessageMutationWithError(context.Background(), m.ID, applyErr.Error(), nextAttempt); err != nil {
		log.Printf("message-mutation: save failure id=%s: %v", m.ID, err)
		return
	}
	log.Printf("message-mutation: %s message=%d failed; retry at %s: %v", m.Kind, m.MessageID, nextAttempt.Format(time.RFC3339), applyErr)
}

// storeIMAPFlagBatch sets or clears the flag on every message's UID with one
// login and one STORE, so they succeed or fail together.
func (h *Handler) storeIMAPFlagBatch(ctx context.Context, lead storage.MessageMutation, infos []storage.MessageMutationInfo) []error {
	op, flag := goimap.StoreFlagsDel, goimap.FlagSeen
	if lead.TargetValue {
		op = goimap.StoreFlagsAdd
	}
	if lead.Kind == storage.MessageMutationStarred {
		flag = goimap.FlagFlagged
	}
	uids := make([]uint32, len(infos))
	for i, info := range infos {
		uids[i] = info.RemoteUID
	}
	err := func() error {
		if h.accountStore == nil {
			return errors.New("IMAP account storage is not available")
		}
		cfg, err := h.accountStore.GetConfig(ctx, lead.AccountID)
		if err != nil {
			return err
		}
		password, err := h.resolvePassword(ctx, cfg, lead.AccountID)
		if err != nil {
			return err
		}
		factory := h.messageMutationIMAPFactory
		if factory == nil {
			factory = func(ctx context.Context, cfg *models.AccountConfig, password string) (messageMutationIMAPClient, error) {
				return imap.NewClient(ctx, cfg, password)
			}
		}
		client, err := factory(ctx, cfg, password)
		if err != nil {
			return err
		}
		defer client.Close()
		return client.StoreFlagsBatch(ctx, infos[0].FolderRemoteID, uids, op, []goimap.Flag{flag})
	}()
	if err != nil {
		return sameMutationError(len(infos), err)
	}
	return make([]error, len(infos))
}

// setJMAPFlagBatch sets or clears the keyword on every message's emails in
// as few Email/set requests as the server allows; each message gets its own
// result.
func (h *Handler) setJMAPFlagBatch(ctx context.Context, lead storage.MessageMutation, infos []storage.MessageMutationInfo) []error {
	if !providers.JMAPEnabled() {
		return sameMutationError(len(infos), providers.ErrJMAPDisabled)
	}
	cfg, err := h.accountStore.GetConfig(ctx, lead.AccountID)
	if err != nil {
		return sameMutationError(len(infos), err)
	}
	password, err := h.accountStore.DecryptPassword(ctx, lead.AccountID)
	if err != nil {
		return sameMutationError(len(infos), err)
	}
	emails := make([]mail.JMAPFlagEmail, len(infos))
	for i, info := range infos {
		emails[i] = mail.JMAPFlagEmail{EmailID: info.RemoteMessageID, FolderCopyIDs: info.FolderCopyIDs}
	}
	failures, err := mail.ApplyJMAPFlagBatch(ctx, cfg, password, lead.Kind, lead.TargetValue, emails)
	if err != nil {
		return sameMutationError(len(infos), err)
	}
	return failures
}

func sameMutationError(n int, err error) []error {
	errs := make([]error, n)
	for i := range errs {
		errs[i] = err
	}
	return errs
}
