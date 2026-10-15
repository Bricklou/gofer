package handler

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/calendar"
	"github.com/cristianadrielbraun/gofer/internal/config"
	"github.com/cristianadrielbraun/gofer/internal/mail/message"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	ical "github.com/emersion/go-ical"
)

func calendarMeetingEvent(component *ical.Component) ical.Event {
	return ical.Event{Component: component}
}

// A CalDAV series meeting the organizer owns: Gofer writes the invitations
// and cancellations for the whole series and for single dates itself.
func TestCalendarCalDAVSeriesMeetingDatesAndSeries(t *testing.T) {
	h := calendarCreateFixture(t)
	body, etag, writes, resource := "", `"v0"`, 0, ""
	deleted := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
		case "PROPFIND":
			props := `<d:current-user-principal><d:href>/principal/</d:href></d:current-user-principal>`
			if r.URL.Path == "/principal/" {
				props = `<c:calendar-user-address-set><d:href>mailto:one@example.com</d:href></c:calendar-user-address-set>`
			}
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, r.URL.Path, props)
		case "REPORT":
			// The server proves a date belongs to the series by expanding it.
			cal, err := calendarUpdateDecodeICS([]byte(body))
			if err != nil {
				t.Error(err)
				return
			}
			uid, _ := calendarMeetingMaster(cal).Props.Text("UID")
			var instances strings.Builder
			for week := 0; week < 4; week++ {
				at := time.Date(2026, 10, 2+7*week, 7, 0, 0, 0, time.UTC)
				fmt.Fprintf(&instances, "BEGIN:VEVENT\r\nUID:%s\r\nRECURRENCE-ID:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:Planning\r\nEND:VEVENT\r\n", uid, at.Format("20060102T150405Z"), at.Format("20060102T150405Z"), at.Add(time.Hour).Format("20060102T150405Z"))
			}
			var data strings.Builder
			_ = xml.EscapeText(&data, []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n"+instances.String()+"END:VCALENDAR\r\n"))
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>%s</d:getetag><c:calendar-data>%s</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, resource, etag, data.String())
		case "PUT":
			if (body == "") != (r.Header.Get("If-None-Match") == "*") || (body != "" && r.Header.Get("If-Match") != etag) {
				t.Error("unsafe meeting write")
			}
			writes++
			resource = r.URL.Path
			data, _ := io.ReadAll(r.Body)
			body = string(data)
			etag = fmt.Sprintf(`"v%d"`, writes)
			w.Header().Set("ETag", etag)
			if writes == 1 {
				w.WriteHeader(201)
			} else {
				w.WriteHeader(204)
			}
		case "GET":
			if deleted || body == "" {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("ETag", etag)
			io.WriteString(w, body)
		case "DELETE":
			if r.Header.Get("If-Match") != etag {
				t.Error("unsafe series cancellation")
			}
			deleted = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	prior := davHTTPTransport
	davHTTPTransport = server.Client().Transport
	defer func() { davHTTPTransport = prior }()
	source := storage.CalendarSource{ID: "one-source", UserID: "one", AccountID: "one-account", Provider: "caldav", RemoteID: server.URL + "/cal/", AccessRole: "owner", IsSelected: true}
	if _, err := h.db.Write().Exec(`UPDATE accounts SET provider='imap',smtp_host='smtp.example.com',smtp_port=587 WHERE id='one-account'; UPDATE calendar_sources SET provider='caldav',remote_id=?,access_role='owner' WHERE id='one-source'`, source.RemoteID); err != nil {
		t.Fatal(err)
	}
	var err error
	h.accountStore, err = config.NewAccountStore(h.db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.accountStore.SaveCalDAVConfig(t.Context(), "one", "one-account", source.RemoteID, "user", "pass", false); err != nil {
		t.Fatal(err)
	}
	credentials := calendarCredentials{username: "user", password: "pass", baseURL: source.RemoteID}
	type sent struct {
		send       storage.OutgoingSend
		calendar   string
		recipients string
	}
	seen := map[string]bool{}
	// Each new email, checked as the sender would just before sending it.
	newSends := func() []sent {
		rows, err := h.db.Read().Query(`SELECT id FROM outgoing_sends ORDER BY created_at, id`)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		var result []sent
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			send, _ := h.db.GetOutgoingSend(t.Context(), id)
			var snapshot outgoingMessageSnapshot
			if err := json.Unmarshal(send.MessageJSON, &snapshot); err != nil {
				t.Fatal(err)
			}
			unlock, err := h.beforeCalendarNotificationSend(t.Context(), send, snapshot.outgoingMessage())
			unlock()
			if err != nil {
				t.Fatalf("%s to %v not allowed: %v", snapshot.CalendarNotification.Method, send.EnvelopeRecipients, err)
			}
			result = append(result, sent{send, snapshot.CalendarNotification.Method + "\n" + snapshot.CalendarNotification.Calendar, strings.Join(send.EnvelopeRecipients, ",")})
		}
		return result
	}
	// Emails queued together share a timestamp; put them in the order wanted.
	expect := func(step string, got []sent, want ...string) []sent {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d emails, want %d", step, len(got), len(want))
		}
		ordered := make([]sent, len(want))
		for i, recipients := range want {
			found := false
			for _, email := range got {
				if email.recipients == recipients {
					ordered[i], found = email, true
				}
			}
			if !found {
				t.Fatalf("%s: no email to %s", step, recipients)
			}
		}
		return ordered
	}

	// A new weekly meeting: one invitation for the whole series.
	draft := calendarProviderDraft(t, false)
	draft.Recurrence = &calendar.RecurrenceDraft{Frequency: "weekly", Interval: 1, Count: 4}
	draft.GuestsSet, draft.Guests = true, []calendar.GuestDraft{{Email: "guest@example.com"}}
	created, err := h.createCalDAVMeeting(t.Context(), source, credentials, draft)
	if err != nil {
		t.Fatal(err)
	}
	invites := expect("create", newSends(), "guest@example.com")
	if !strings.Contains(invites[0].calendar, "RRULE:") {
		t.Fatal("series invitation without its repeat rule")
	}
	endpoint := created.RemoteID
	series := func() storage.CalendarEvent {
		cal, _ := calendarUpdateDecodeICS([]byte(body))
		event, err := calendarMeetingNormalized(cal, http.Header{"Etag": {etag}}, endpoint, "one@example.com")
		if err != nil {
			t.Fatal(err)
		}
		stored := calendarStorageEvent("one", "one-source", event)
		stored.SourceProvider, stored.AccountEmail = "caldav", "one@example.com"
		return stored
	}

	// The whole series, with a second guest.
	draft.Summary = "Weekly planning"
	draft.Guests = append(draft.Guests, calendar.GuestDraft{Email: "new@example.com"})
	if _, err := h.updateCalDAVMeeting(t.Context(), source, credentials, series(), draft); err != nil {
		t.Fatal(err)
	}
	expect("series edit", newSends(), "guest@example.com,new@example.com")

	// The second date: an hour later, without the second guest and with a third.
	date := func(day int) storage.CalendarEvent {
		at := time.Date(2026, 10, day, 7, 0, 0, 0, time.UTC)
		end := at.Add(time.Hour)
		event := series()
		event.RemoteID = endpoint + "#recurrence=" + url.QueryEscape(at.Format(time.RFC3339))
		event.SeriesRemoteID, event.RecurrenceJSON = endpoint, `["RECURRENCE-ID:`+at.Format(time.RFC3339)+`"]`
		event.StartAt, event.EndAt = &at, &end
		return event
	}
	one := draft
	one.Recurrence = nil
	moved := date(9).StartAt.Add(time.Hour)
	movedEnd := moved.Add(time.Hour)
	one.StartAt, one.EndAt = &moved, &movedEnd
	one.Guests = []calendar.GuestDraft{{Email: "guest@example.com"}, {Email: "third@example.com"}}
	saved, err := h.updateCalDAVMeetingOccurrence(t.Context(), source, credentials, date(9), &one)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SeriesRemoteID != endpoint || !saved.StartAt.Equal(moved) || !calendarMeetingGuestsMatch(saved.Attendees, one.Guests, "one@example.com") {
		t.Fatalf("saved date %+v", saved)
	}
	emails := expect("date edit", newSends(), "guest@example.com,third@example.com", "new@example.com")
	for _, email := range emails {
		if !strings.Contains(email.calendar, "RECURRENCE-ID") || strings.Contains(email.calendar, "RRULE") {
			t.Fatal("a date's email is not for just that date")
		}
	}
	if !strings.Contains(emails[1].calendar, "CANCEL") {
		t.Fatal("the removed guest got no cancellation for the date")
	}

	// The whole series opens for editing with a date changed on its own. A new
	// title reaches that date, which keeps its own time and guests; the one
	// invitation covers the series and the date, so it reaches all guests.
	cal, _ := calendarUpdateDecodeICS([]byte(body))
	if _, err := calendarCalDAVMeetingSeriesEvent(cal, http.Header{"Etag": {etag}}, endpoint, "one@example.com"); err != nil {
		t.Fatal(err)
	}
	draft.Summary = "Team planning"
	if _, err := h.updateCalDAVMeeting(t.Context(), source, credentials, series(), draft); err != nil {
		t.Fatal(err)
	}
	expect("series edit with a changed date", newSends(), "guest@example.com,new@example.com,third@example.com")
	cal, _ = calendarUpdateDecodeICS([]byte(body))
	changed := calendarMeetingComponent(cal, date(9).StartAt.Format(time.RFC3339))
	if changed == nil {
		t.Fatal("the changed date was lost")
	}
	kept, err := normalizeCalDAVEvent(calendarMeetingEvent(changed), endpoint, etag, time.UTC)
	if err != nil || kept.Summary != "Team planning" || !kept.StartAt.Equal(moved) || !calendarMeetingGuestsMatch(kept.Attendees, one.Guests, "one@example.com") {
		t.Fatalf("the changed date did not keep its time and guests and take the new title: %+v %v", kept, err)
	}

	// The third date is removed: its guests get a cancellation for it.
	if _, err := h.updateCalDAVMeetingOccurrence(t.Context(), source, credentials, date(16), nil); err != nil {
		t.Fatal(err)
	}
	cancel := expect("date delete", newSends(), "guest@example.com,new@example.com")
	if !strings.Contains(cancel[0].calendar, "CANCEL") || !strings.Contains(cancel[0].calendar, "RECURRENCE-ID") || !strings.Contains(body, "EXDATE") {
		t.Fatal("removed date not cancelled for just that date")
	}

	// A new time can't keep the dates changed or removed one by one: nothing
	// is saved until the organizer confirms resetting them.
	later := draft.StartAt.Add(30 * time.Minute)
	laterEnd := draft.EndAt.Add(30 * time.Minute)
	draft.StartAt, draft.EndAt = &later, &laterEnd
	before := writes
	if _, err := h.updateCalDAVMeeting(t.Context(), source, credentials, series(), draft); !errors.Is(err, errCalendarSeriesDatesReset) || writes != before {
		t.Fatalf("time change without confirmation: %v, %d writes", err, writes-before)
	}
	expect("unconfirmed reset", newSends())
	draft.ResetSeriesDates = true
	if _, err := h.updateCalDAVMeeting(t.Context(), source, credentials, series(), draft); err != nil {
		t.Fatal(err)
	}
	reset := expect("confirmed reset", newSends(), "guest@example.com,new@example.com", "third@example.com")
	if strings.Contains(body, "RECURRENCE-ID") || strings.Contains(body, "EXDATE") || strings.Contains(reset[0].calendar, "RECURRENCE-ID") || !strings.Contains(reset[1].calendar, "CANCEL") {
		t.Fatalf("dates not reset: %s", body)
	}

	// Deleting the series reaches everyone invited to any date.
	if err := h.deleteCalDAVMeeting(t.Context(), source, credentials, series()); err != nil {
		t.Fatal(err)
	}
	deleted = true
	expect("series delete", newSends(), "guest@example.com,new@example.com")
}

// Guests' replies to a CalDAV series Gofer schedules: for the whole series, or
// for one date, which gets its own event the first time.
func TestCalendarCalDAVSeriesReplies(t *testing.T) {
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:series-uid\r\nSEQUENCE:1\r\nDTSTAMP:20261001T000000Z\r\nDTSTART:20261002T070000Z\r\nDTEND:20261002T080000Z\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\nSUMMARY:Planning\r\nORGANIZER;SCHEDULE-AGENT=CLIENT:mailto:one@example.com\r\nATTENDEE;PARTSTAT=NEEDS-ACTION;SCHEDULE-AGENT=CLIENT:mailto:guest@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	etag, writes := `"v0"`, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "REPORT":
			var instances strings.Builder
			for week := 0; week < 4; week++ {
				at := time.Date(2026, 10, 2+7*week, 7, 0, 0, 0, time.UTC)
				fmt.Fprintf(&instances, "BEGIN:VEVENT\r\nUID:series-uid\r\nRECURRENCE-ID:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:Planning\r\nEND:VEVENT\r\n", at.Format("20060102T150405Z"), at.Format("20060102T150405Z"), at.Add(time.Hour).Format("20060102T150405Z"))
			}
			var data strings.Builder
			_ = xml.EscapeText(&data, []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n"+instances.String()+"END:VCALENDAR\r\n"))
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>/cal/series.ics</d:href><d:propstat><d:prop><d:getetag>%s</d:getetag><c:calendar-data>%s</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, etag, data.String())
		case "PUT":
			if r.Header.Get("If-Match") != etag {
				t.Error("unsafe reply merge")
			}
			writes++
			data, _ := io.ReadAll(r.Body)
			body, etag = string(data), fmt.Sprintf(`"v%d"`, writes)
			w.Header().Set("ETag", etag)
			w.WriteHeader(204)
		case "GET":
			w.Header().Set("ETag", etag)
			io.WriteString(w, body)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	prior := davHTTPTransport
	davHTTPTransport = server.Client().Transport
	defer func() { davHTTPTransport = prior }()
	source := storage.CalendarSource{ID: "source", UserID: "one", AccountID: "account", Provider: "caldav", RemoteID: server.URL + "/cal/", AccessRole: "owner"}
	credentials := calendarCredentials{username: "user", password: "pass", baseURL: source.RemoteID}
	endpoint := server.URL + "/cal/series.ics"
	date := func(day int) storage.CalendarEvent {
		at := time.Date(2026, 10, day, 7, 0, 0, 0, time.UTC)
		end := at.Add(time.Hour)
		return storage.CalendarEvent{ID: fmt.Sprint("date-", day), UserID: "one", SourceID: "source", SourceProvider: "caldav", RemoteID: endpoint + "#recurrence=" + url.QueryEscape(at.Format(time.RFC3339)),
			SeriesRemoteID: endpoint, ETag: etag, ICalUID: "series-uid", OrganizerEmail: "one@example.com", AccountEmail: "one@example.com", ResponseStatus: "organizer",
			AttendeesJSON: `[{"email":"guest@example.com"}]`, RecurrenceJSON: `["RECURRENCE-ID:` + at.Format(time.RFC3339) + `"]`, StartAt: &at, EndAt: &end}
	}
	reply := func(status, recurrence string, sequence int) message.CalendarIncomingReply {
		return message.CalendarIncomingReply{UID: "series-uid", Organizer: "one@example.com", Attendee: "guest@example.com", From: "guest@example.com", Status: status, RecurrenceID: recurrence, Sequence: sequence, Stamp: time.Now().UTC()}
	}
	apply := func(existing storage.CalendarEvent, r message.CalendarIncomingReply) (storage.CalendarEvent, error) {
		var published storage.CalendarEvent
		err := applyCalendarIncomingCalDAV(t.Context(), source, existing, r, credentials, func(context.Context) (bool, error) { return true, nil }, func(_ context.Context, row storage.CalendarEvent) error {
			published = row
			return nil
		})
		return published, err
	}
	status := func(key string) string {
		cal, _ := calendarUpdateDecodeICS([]byte(body))
		component := calendarMeetingComponent(cal, key)
		if component == nil {
			return ""
		}
		for _, person := range component.Props["ATTENDEE"] {
			if calendarReplyAddress(person.Value) == "guest@example.com" {
				return person.Params.Get("PARTSTAT")
			}
		}
		return "missing"
	}

	// Accepting the series changes the series' own event.
	row, err := apply(date(9), reply("ACCEPTED", "", 1))
	if err != nil || status("") != "ACCEPTED" || row.ID != "date-9" || !strings.Contains(row.AttendeesJSON, `"status":"ACCEPTED"`) || row.ETag != etag {
		t.Fatalf("series reply: %v, %+v, %s", err, row, body)
	}
	// Declining one date gives it its own event, and keeps the series accepted.
	third := time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
	row, err = apply(date(16), reply("DECLINED", third, 1))
	if err != nil || status(third) != "DECLINED" || status("") != "ACCEPTED" || !strings.Contains(row.AttendeesJSON, `"status":"DECLINED"`) {
		t.Fatalf("date reply: %v, %+v, %s", err, row, body)
	}
	// Accepting it again changes that date's own event.
	if _, err := apply(date(16), reply("ACCEPTED", third, 1)); err != nil || status(third) != "ACCEPTED" {
		t.Fatalf("second date reply: %v, %s", err, body)
	}
	// A reply naming another date than the cached one, or an older revision, is ignored.
	before := writes
	if _, err := apply(date(9), reply("DECLINED", time.Date(2026, 10, 23, 7, 0, 0, 0, time.UTC).Format(time.RFC3339), 1)); !errors.Is(err, errCalendarIncomingIgnored) {
		t.Fatalf("reply for an uncached date: %v", err)
	}
	if _, err := apply(date(9), reply("DECLINED", "", 0)); !errors.Is(err, errCalendarIncomingIgnored) || writes != before {
		t.Fatalf("outdated reply: %v", err)
	}
}
