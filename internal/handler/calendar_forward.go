package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/message"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	ical "github.com/emersion/go-ical"
	"github.com/google/uuid"
)

// Forwarding passes an invitation you received on to someone else. Microsoft
// does it itself; for CalDAV, Gofer emails the invitation from your account,
// the way other mail apps do. Google has no way to do it, so it is not offered.

const calendarForwardMaxRecipients = 50

type calendarForwardFailure struct {
	status  int
	message string
}

func (f calendarForwardFailure) Error() string { return f.message }

func calendarForwardProvider(provider string) bool {
	return provider == providers.ProviderOutlook || provider == storage.CalendarSourceProviderCalDAV
}

// The whole meeting is forwarded: a series, when it repeats.
func calendarForwardScope(event storage.CalendarEvent) string {
	if calendarEventIsSeries(event) {
		return "series"
	}
	return "event"
}

// The form: who to forward to, separated by commas or lines, and an optional
// message to them.
func parseCalendarForwardForm(r *http.Request) ([]*mail.Address, string, error) {
	if r.URL.RawQuery != "" || r.ParseForm() != nil || len(r.PostForm) != 2 || len(r.PostForm["recipients"]) != 1 || len(r.PostForm["comment"]) != 1 {
		return nil, "", calendarForwardFailure{http.StatusBadRequest, "Invalid forward form. Reopen the invitation."}
	}
	comment := strings.TrimSpace(r.PostForm.Get("comment"))
	if len(comment) > 4000 {
		return nil, "", calendarForwardFailure{http.StatusBadRequest, "The message is too long."}
	}
	raw := strings.NewReplacer("\r\n", ",", "\n", ",", ";", ",").Replace(r.PostForm.Get("recipients"))
	var parts []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil, "", calendarForwardFailure{http.StatusBadRequest, "Add who to forward the invitation to."}
	}
	if len(parts) > calendarForwardMaxRecipients {
		return nil, "", calendarForwardFailure{http.StatusBadRequest, fmt.Sprintf("Forward to at most %d people at once.", calendarForwardMaxRecipients)}
	}
	seen := map[string]bool{}
	var recipients []*mail.Address
	for _, part := range parts {
		address, err := mail.ParseAddress(part)
		if err != nil || calendarReplyAddress("mailto:"+address.Address) == "" {
			return nil, "", calendarForwardFailure{http.StatusBadRequest, fmt.Sprintf("%q is not an email address.", part)}
		}
		key := strings.ToLower(address.Address)
		if !seen[key] {
			seen[key] = true
			recipients = append(recipients, address)
		}
	}
	return recipients, comment, nil
}

// forwardCalendarInvitation sends the invitation the target was read from to
// the recipients. Returns how it went out: "provider" or "email".
func (h *Handler) forwardCalendarInvitation(ctx context.Context, source storage.CalendarSource, target calendarResponseTarget, recipients []*mail.Address, comment string, queue func(storage.QueueOutgoingSendInput) error) (string, error) {
	switch {
	case source.Provider == providers.ProviderOutlook && target.Outlook != nil:
		return "provider", forwardOutlookInvitation(ctx, target, recipients, comment)
	case source.Provider == storage.CalendarSourceProviderCalDAV && target.CalDAV != nil:
		account, err := h.calendarReplyAccount(ctx, source)
		if err != nil || account.Email == "" {
			return "", calendarForwardFailure{http.StatusConflict, "This account's sending address could not be found."}
		}
		body, err := calendarForwardICS(target.CalDAV.Calendar, recipients)
		if err != nil {
			return "", err
		}
		msg := calendarForwardMessage(account.Name, account.Email, target.Event.Summary, comment, body, recipients)
		raw, err := buildOutgoingMIME(storage.OutgoingTransportSMTP, msg)
		if err != nil {
			return "", err
		}
		snapshot, err := json.Marshal(snapshotOutgoingMessage(msg))
		if err != nil {
			return "", err
		}
		var to []string
		for _, recipient := range recipients {
			to = append(to, recipient.Address)
		}
		return "email", queue(storage.QueueOutgoingSendInput{ID: uuid.NewString(), AccountID: source.AccountID, Transport: storage.OutgoingTransportSMTP, EnvelopeFrom: account.Email, EnvelopeRecipients: to, MIMEData: raw, MessageJSON: snapshot})
	}
	return "", calendarForwardFailure{http.StatusBadRequest, "This calendar can't forward invitations."}
}

// Graph acknowledges a forward with 202 and no body.
// https://learn.microsoft.com/graph/api/event-forward?view=graph-rest-1.0
func forwardOutlookInvitation(ctx context.Context, target calendarResponseTarget, recipients []*mail.Address, comment string) error {
	type address struct {
		Address string `json:"address"`
		Name    string `json:"name,omitempty"`
	}
	type recipient struct {
		EmailAddress address `json:"emailAddress"`
	}
	payload := struct {
		ToRecipients []recipient `json:"toRecipients"`
		Comment      string      `json:"comment"`
	}{Comment: comment}
	for _, person := range recipients {
		payload.ToRecipients = append(payload.ToRecipients, recipient{address{person.Address, person.Name}})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Endpoint+"/forward", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+target.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := calendarProviderDo(client, req)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return calendarUpdateHTTPError(calendarCreateProviderError{response.StatusCode})
	}
	return nil
}

// The invitation as received, sent on as a REQUEST that also invites the
// recipients, so that they can answer it. The organizer stays the same.
func calendarForwardICS(source *ical.Calendar, recipients []*mail.Address) (string, error) {
	if source == nil || len(source.Events()) == 0 {
		return "", errors.New("no invitation to forward")
	}
	encoded, err := calendarNotificationICS(source, "REQUEST", nil)
	if err != nil {
		return "", err
	}
	cal, err := calendarUpdateDecodeICS([]byte(encoded))
	if err != nil {
		return "", err
	}
	for _, event := range cal.Events() {
		invited := map[string]bool{}
		for _, person := range event.Props["ATTENDEE"] {
			invited[calendarReplyAddress(person.Value)] = true
		}
		for _, recipient := range recipients {
			if invited[strings.ToLower(recipient.Address)] {
				continue
			}
			person := ical.NewProp("ATTENDEE")
			person.Value = "mailto:" + recipient.Address
			if recipient.Name != "" {
				person.Params.Set("CN", recipient.Name)
			}
			person.Params.Set("ROLE", "REQ-PARTICIPANT")
			person.Params.Set("PARTSTAT", "NEEDS-ACTION")
			person.Params.Set("RSVP", "TRUE")
			event.Props.Add(person)
		}
	}
	return encodeCalendarReply(cal)
}

func calendarForwardMessage(fromName, fromEmail, summary, comment, invitation string, recipients []*mail.Address) *message.OutgoingMessage {
	summary = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(summary))
	if summary == "" {
		summary = "Meeting"
	}
	sender := strings.TrimSpace(fromName)
	if sender == "" {
		sender = fromEmail
	}
	text := sender + " forwarded you an invitation: " + summary
	if comment != "" {
		text = comment + "\n\n" + text
	}
	msg := &message.OutgoingMessage{FromName: fromName, FromEmail: fromEmail, Subject: "Fwd: Invitation: " + summary, TextBody: text, CalendarForward: invitation, MessageID: message.NewMessageID(), Date: time.Now().UTC()}
	msg.To = append(msg.To, recipients...)
	return msg
}

func calendarForwardDone(w http.ResponseWriter, delivery string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"forwarded": true, "delivery": delivery})
}

func calendarForwardError(w http.ResponseWriter, err error) {
	var failure calendarForwardFailure
	if errors.As(err, &failure) {
		calendarUpdateFailure(w, failure.status, failure.message, false, false)
		return
	}
	var configuration calendarReplyConfigurationError
	if errors.As(err, &configuration) {
		calendarUpdateFailure(w, http.StatusBadRequest, configuration.Error(), false, false)
		return
	}
	if errors.Is(err, errCalendarUpdateConflict) || errors.Is(err, errCalendarNotInvitation) {
		calendarUpdateFailure(w, http.StatusConflict, "This invitation changed. Reopen it before forwarding; nothing was sent.", false, true)
		return
	}
	if calendarCreateUncertain(err) {
		calendarUpdateFailure(w, http.StatusBadGateway, "The forward could not be confirmed. Check your sent invitations before trying again.", true, false)
		return
	}
	calendarUpdateFailure(w, http.StatusBadGateway, "Could not forward the invitation. Check Calendar access and try again.", false, false)
}

func (h *Handler) handleCalendarForward(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	recipients, comment, err := parseCalendarForwardForm(r)
	if err != nil {
		calendarForwardError(w, err)
		return
	}
	userID := h.userID(r.Context())
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), calendarSourceTimeout)
	defer cancel()
	event, err := h.db.GetCalendarEvent(ctx, userID, r.PathValue("id"))
	if err != nil {
		calendarUpdateFailure(w, http.StatusNotFound, "This invitation is no longer available. Refresh the calendar.", false, true)
		return
	}
	source, err := h.calendarResponseAccess(ctx, event)
	if err != nil || !calendarForwardProvider(source.Provider) {
		calendarUpdateFailure(w, http.StatusForbidden, "Forwarding is unavailable for this event.", false, false)
		return
	}
	if source.Provider == storage.CalendarSourceProviderCalDAV {
		if cfg, err := h.accountStore.GetConfig(ctx, source.AccountID); err != nil || cfg.SMTPHost == "" || cfg.SMTPPort <= 0 {
			calendarUpdateFailure(w, http.StatusBadRequest, "Forwarding sends the invitation by email. Configure SMTP for this account first.", false, false)
			return
		}
	}
	target, err := h.readCalendarResponse(ctx, source, event, calendarForwardScope(event))
	if err != nil {
		calendarForwardError(w, err)
		return
	}
	delivery, err := h.forwardCalendarInvitation(ctx, source, target, recipients, comment, func(input storage.QueueOutgoingSendInput) error {
		_, err := h.db.QueueOutgoingSend(ctx, input)
		return err
	})
	if err != nil {
		calendarForwardError(w, err)
		return
	}
	if delivery == "email" {
		h.signalOutgoingWorker()
	}
	calendarForwardDone(w, delivery)
}
