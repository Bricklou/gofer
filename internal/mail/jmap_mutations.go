package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// JMAPMutation is one queued change to a JMAP email, with the server ids it
// needs: the email, and for a move the mailboxes it leaves and enters.
type JMAPMutation struct {
	Kind          string
	TargetValue   bool
	EmailID       string
	FromMailboxID string
	ToMailboxID   string
}

// ApplyJMAPMutation sends one queued change to the JMAP server as a single
// Email/set. Read and starred set or clear one keyword; a move swaps one
// mailbox for another, leaving others the email is in; a delete destroys the
// email for good, as Gofer's delete does for Gmail and Outlook. It reports
// gone when the server no longer has the email, which needs no retry.
func ApplyJMAPMutation(ctx context.Context, cfg *models.AccountConfig, password string, m JMAPMutation) (gone bool, err error) {
	if m.EmailID == "" {
		return false, errors.New("JMAP email identity is unavailable")
	}
	client, err := newJMAPAccountClient(cfg, password)
	if err != nil {
		return false, err
	}
	session, err := client.Session(ctx, cfg.JMAPSessionURL)
	if err != nil {
		return false, fmt.Errorf("JMAP session: %w", err)
	}
	args := jmap.EmailSet{AccountID: session.PrimaryAccount(jmap.CapabilityMail)}
	if args.AccountID == "" {
		return false, errors.New("the JMAP server offers no mail account for this login")
	}
	switch m.Kind {
	case storage.MessageMutationRead:
		args.Update = map[string]jmap.PatchObject{m.EmailID: jmap.KeywordPatch(jmap.KeywordSeen, m.TargetValue)}
	case storage.MessageMutationStarred:
		args.Update = map[string]jmap.PatchObject{m.EmailID: jmap.KeywordPatch(jmap.KeywordFlagged, m.TargetValue)}
	case storage.MessageMutationMove:
		if m.ToMailboxID == "" {
			return false, errors.New("JMAP move destination has no mailbox identity")
		}
		args.Update = map[string]jmap.PatchObject{m.EmailID: jmap.MovePatch(m.FromMailboxID, m.ToMailboxID)}
	case storage.MessageMutationDelete:
		args.Destroy = []string{m.EmailID}
	default:
		return false, fmt.Errorf("unsupported JMAP mutation %q", m.Kind)
	}
	result, err := client.SetEmails(ctx, session, args)
	if err != nil {
		return false, err
	}
	failure, failed := result.NotUpdated[m.EmailID]
	if m.Kind == storage.MessageMutationDelete {
		failure, failed = result.NotDestroyed[m.EmailID]
	}
	if !failed {
		return false, nil
	}
	if failure.Type == "notFound" {
		return true, nil
	}
	return false, &failure
}

// applyJMAPMutation replays one queued change of a JMAP account in a routed
// worker, then records it as IMAP changes are recorded.
func (s *UserIMAP) applyJMAPMutation(ctx context.Context, scope *userIMAPScope, mutation storage.MessageMutation, info storage.MessageMutationInfo) error {
	if mutation.AccountID != scope.id || info.AccountID != scope.id || mutation.ProviderType != storage.MessageMutationProviderJMAP || info.AccountProvider != "jmap" {
		return errors.New("queued mutation account or provider changed")
	}
	request := JMAPMutation{Kind: mutation.Kind, TargetValue: mutation.TargetValue, EmailID: info.RemoteMessageID}
	if mutation.Kind == storage.MessageMutationMove {
		if err := scope.call(ctx, func(db *storage.DB) error {
			var err error
			if request.FromMailboxID, err = db.GetFolderProviderRemoteID(ctx, mutation.FolderID); err != nil {
				return err
			}
			request.ToMailboxID, err = db.GetFolderProviderRemoteID(ctx, mutation.DestinationFolderID)
			return err
		}); err != nil {
			return err
		}
	}
	gone, err := ApplyJMAPMutation(ctx, scope.config, scope.password, request)
	if err != nil {
		return err
	}
	return scope.call(ctx, func(db *storage.DB) error {
		switch {
		case mutation.Kind == storage.MessageMutationDelete:
			if err := db.CompleteMessageDeleteMutation(ctx, mutation.ID); err != nil {
				return err
			}
			if gone {
				return db.ConfirmMessageDeleteMutation(ctx, mutation.ID)
			}
			return nil
		case mutation.Kind == storage.MessageMutationMove && !gone:
			// JMAP ids stay the same when an email changes mailbox.
			if err := db.AdvanceMessageMoveMutation(ctx, mutation.ID, mutation.DestinationFolderID, request.EmailID, 0); err != nil {
				return err
			}
		}
		return db.CompleteMessageMutation(ctx, mutation.ID)
	})
}
