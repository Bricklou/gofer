package mail

import (
	"context"
	"errors"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/imap"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	goimap "github.com/emersion/go-imap/v2"
)

// mutationFlagBatchLimit is how many read or starred changes one replay
// sends to the provider together.
const mutationFlagBatchLimit = 500

// applyFlagBatch sends lead and the account's other due changes of the same
// flag, value and folder to the provider in one request: one IMAP STORE over
// their UIDs, or Email/set calls naming many emails on JMAP. A change whose
// message must be looked up on the server first takes the one-by-one path,
// and one whose message is gone is dropped. It gives back the changes that
// failed; err is set when the queue itself could not be read or written.
func (s *UserIMAP) applyFlagBatch(ctx context.Context, scope *userIMAPScope, lead storage.MessageMutation) (failed []mutationFailure, err error) {
	batch := []storage.MessageMutation{lead}
	infos := make([]*storage.MessageMutationInfo, 0, mutationFlagBatchLimit)
	var validity uint32
	if err := scope.call(ctx, func(db *storage.DB) error {
		more, err := db.ClaimMessageFlagBatch(ctx, lead, time.Now(), mutationFlagBatchLimit-1)
		if err != nil {
			return err
		}
		batch = append(batch, more...)
		for _, m := range batch {
			var info *storage.MessageMutationInfo
			if m.FolderID == "" {
				info, err = db.GetMessageMutationInfoInternal(ctx, m.MessageID)
			} else {
				info, err = db.GetMessageMutationInfoInFolder(ctx, m.MessageID, m.FolderID)
			}
			if err != nil {
				return err
			}
			infos = append(infos, info)
		}
		if lead.ProviderType == storage.MessageMutationProviderIMAP {
			validity, err = db.GetFolderUIDValidity(ctx, lead.FolderID)
		}
		return err
	}); err != nil {
		// The claimed rows stay processing and recover on the next pass.
		return nil, err
	}

	var gone, single, grouped []storage.MessageMutation
	var groupedInfos []storage.MessageMutationInfo
	for i, m := range batch {
		info := infos[i]
		switch {
		case info == nil:
			gone = append(gone, m)
		case m.AccountID != scope.id || info.AccountID != scope.id || info.AccountProvider != m.ProviderType:
			// As applyMutation checks: the account is still the same IMAP or JMAP one.
			failed = append(failed, mutationFailure{m, errors.New("queued mutation account or provider changed")})
		case m.ProviderType == storage.MessageMutationProviderIMAP && info.RemoteUID == 0,
			m.ProviderType == storage.MessageMutationProviderJMAP && info.RemoteMessageID == "":
			single = append(single, m)
		case m.ProviderType == storage.MessageMutationProviderIMAP && info.FolderRemoteID == "":
			failed = append(failed, mutationFailure{m, errors.New("message has no remote IMAP folder identity")})
		default:
			grouped = append(grouped, m)
			groupedInfos = append(groupedInfos, *info)
		}
	}
	if len(gone) > 0 {
		if err := scope.call(ctx, func(db *storage.DB) error {
			for _, m := range gone {
				if err := db.DiscardMessageMutation(ctx, m.ID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return failed, err
		}
	}
	for _, m := range single {
		if err := s.applyMutation(ctx, scope, m); err != nil {
			failed = append(failed, mutationFailure{m, err})
		}
	}
	if len(grouped) == 0 {
		return failed, nil
	}

	var groupFailures []error
	switch lead.ProviderType {
	case storage.MessageMutationProviderIMAP:
		groupFailures = s.storeIMAPFlagBatch(ctx, scope, lead, groupedInfos, validity)
	case storage.MessageMutationProviderJMAP:
		emails := make([]JMAPFlagEmail, len(groupedInfos))
		for i, info := range groupedInfos {
			emails[i] = JMAPFlagEmail{EmailID: info.RemoteMessageID, FolderCopyIDs: info.FolderCopyIDs}
		}
		var sendErr error
		groupFailures, sendErr = ApplyJMAPFlagBatch(ctx, scope.config, scope.password, lead.Kind, lead.TargetValue, emails)
		if sendErr != nil {
			groupFailures = sameError(len(grouped), sendErr)
		}
	}
	var applied []storage.MessageMutation
	for i, m := range grouped {
		if groupFailures[i] != nil {
			failed = append(failed, mutationFailure{m, groupFailures[i]})
		} else {
			applied = append(applied, m)
		}
	}
	if err := scope.call(ctx, func(db *storage.DB) error {
		for _, m := range applied {
			if err := db.CompleteMessageMutation(ctx, m.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return failed, err
	}
	return failed, nil
}

// storeIMAPFlagBatch sets or clears the flag on every message's UID in the
// batch's folder, checking the folder's UIDVALIDITY. One STORE covers them
// all, so they succeed or fail together.
func (s *UserIMAP) storeIMAPFlagBatch(ctx context.Context, scope *userIMAPScope, lead storage.MessageMutation, infos []storage.MessageMutationInfo, validity uint32) []error {
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
	client, err := imap.DefaultPool.Get(ctx, scope.config, scope.password)
	if err == nil {
		defer client.Close()
		err = client.StoreFlagsBatchIfUIDValidity(ctx, infos[0].FolderRemoteID, uids, op, []goimap.Flag{flag}, validity)
	}
	if err != nil {
		return sameError(len(infos), err)
	}
	return make([]error, len(infos))
}

func sameError(n int, err error) []error {
	errs := make([]error, n)
	for i := range errs {
		errs[i] = err
	}
	return errs
}
