package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// sendJMAP sends one queued message of a JMAP account and records the result
// as provider sends are recorded: the server keeps the Sent copy, and the
// local one is published by the Sent-copy step.
func (s *userMailDeliveryScope) sendJMAP(ctx context.Context, send storage.OutgoingSend) (bool, error) {
	var snapshot outgoingMessageSnapshot
	err := json.Unmarshal(send.MessageJSON, &snapshot)
	if err == nil && (send.Transport != storage.OutgoingTransportJMAP || snapshot.MessageID == "" || snapshot.CalendarReply != "" || snapshot.CalendarNotification != nil) {
		err = errors.New("unsupported routed JMAP outgoing message")
	}
	var emailID string
	if err == nil {
		work, cancel := context.WithTimeout(ctx, 2*time.Minute)
		emailID, err = mail.SendJMAPMessage(work, s.cfg, s.password, send.EnvelopeFrom, send.EnvelopeRecipients, send.MIMEData, jmapEarlierAttempt(send))
		cancel()
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	} // Leave the claim for recovery, never silently resend.
	status, text := storage.OutgoingSendSent, ""
	next := time.Now().Add(outgoingSendRetryDelayWithJitter(send.AttemptCount, s.runner.h.outgoingRandomValue()))
	if err != nil {
		text, status = sanitizeOutgoingErrorText(err.Error()), storage.OutgoingSendAmbiguous
		var notSent *mail.JMAPNotSentError
		if errors.As(err, &notSent) {
			status = storage.OutgoingSendFailed
			if notSent.Retryable && send.AttemptCount < outgoingSendMaxAttempts {
				status = storage.OutgoingSendPending
			}
		}
		err = errors.New(text)
	}
	finalErr := s.call(ctx, func(db *storage.DB) error {
		switch status {
		case storage.OutgoingSendSent:
			return db.CompleteUserProviderSend(ctx, send, s.cfg.ProviderAccountID, snapshot.MessageID, emailID)
		case storage.OutgoingSendPending:
			return db.FinishOutgoingSendWithRetry(ctx, send.ID, text, next)
		default:
			return db.FinishOutgoingSendWithError(ctx, send.ID, status, text)
		}
	})
	if finalErr != nil {
		return false, errors.Join(err, finalErr)
	}
	eventStatus, payload := status, map[string]any{"send_id": send.ID}
	if status == storage.OutgoingSendPending {
		eventStatus = "retrying"
		payload["attempt"], payload["next_attempt_at"], payload["retry_in_seconds"] = send.AttemptCount, next.Format(time.RFC3339), retryInSeconds(time.Until(next))
	}
	s.runner.h.userIMAP.Events().Publish(mail.Event{Type: mail.EventSendResult, UserID: s.owner, AccountID: s.id, Status: eventStatus, Error: text, Payload: payload})
	return true, err
}
