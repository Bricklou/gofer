package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func itoa(n int) string { raw, _ := json.Marshal(n); return string(raw) }

// Each provider's earliest pop-up reminder is the one Gofer keeps.
func TestCalendarRemindersReadFromEachProvider(t *testing.T) {
	var google googleCalendarEvent
	_ = json.Unmarshal([]byte(`{"id":"g","start":{"dateTime":"2026-11-03T09:00:00Z"},"end":{"dateTime":"2026-11-03T10:00:00Z"},"reminders":{"useDefault":false,"overrides":[{"method":"email","minutes":1440},{"method":"popup","minutes":10},{"method":"popup","minutes":30}]}}`), &google)
	event, err := normalizeGoogleCalendarEvent(google)
	if err != nil || event.ReminderMinutes == nil || *event.ReminderMinutes != 30 {
		t.Fatalf("Google overrides: %v %v", event.ReminderMinutes, err)
	}
	if got := googleCalendarEarliestReminder([]googleCalendarReminder{{Method: "popup", Minutes: 15}}); got == nil || *got != 15 {
		t.Fatal("Google default reminder not read")
	}
	if googleCalendarEarliestReminder([]googleCalendarReminder{{Method: "email", Minutes: 15}}) != nil {
		t.Fatal("an email reminder was read as a pop-up")
	}
	var outlook outlookCalendarEvent
	_ = json.Unmarshal([]byte(`{"id":"o","start":{"dateTime":"2026-11-03T09:00:00","timeZone":"UTC"},"end":{"dateTime":"2026-11-03T10:00:00","timeZone":"UTC"},"isReminderOn":true,"reminderMinutesBeforeStart":15}`), &outlook)
	if event, err := normalizeOutlookCalendarEvent(outlook); err != nil || event.ReminderMinutes == nil || *event.ReminderMinutes != 15 {
		t.Fatalf("Outlook reminder: %v %v", event.ReminderMinutes, err)
	}
	off := false
	outlook.IsReminderOn = &off
	if event, _ := normalizeOutlookCalendarEvent(outlook); event.ReminderMinutes != nil {
		t.Fatal("a reminder that is off was read")
	}
	cal, err := calendarUpdateDecodeICS([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:c\r\nDTSTART:20261103T090000Z\r\nDTEND:20261103T100000Z\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT10M\r\nEND:VALARM\r\nBEGIN:VALARM\r\nACTION:EMAIL\r\nTRIGGER:-P1D\r\nEND:VALARM\r\nBEGIN:VALARM\r\nACTION:AUDIO\r\nTRIGGER;VALUE=DATE-TIME:20261103T083000Z\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if event, err := normalizeCalDAVEvent(cal.Events()[0], "c.ics", `"v1"`, time.UTC); err != nil || event.ReminderMinutes == nil || *event.ReminderMinutes != 30 {
		t.Fatalf("CalDAV alarms: %v %v", event.ReminderMinutes, err)
	}
	// Setting a reminder replaces the display and sound alarms, keeping email ones.
	fifteen := 15
	calDAVSetReminder(cal.Events()[0].Component, &fifteen)
	actions := []string{}
	for _, child := range cal.Events()[0].Children {
		action, _ := child.Props.Text("ACTION")
		trigger := child.Props.Get("TRIGGER")
		actions = append(actions, action+" "+trigger.Value)
	}
	if strings.Join(actions, ",") != "EMAIL -P1D,DISPLAY -PT15M" {
		t.Fatalf("alarms after setting the reminder: %v", actions)
	}
	calDAVSetReminder(cal.Events()[0].Component, nil)
	if len(cal.Events()[0].Children) != 1 {
		t.Fatal("removing the reminder removed the email alarm too")
	}
}

func TestCalendarReminderForm(t *testing.T) {
	for value, want := range map[string]string{"default": "unset", "none": "none", "0": "0", "1440": "1440"} {
		values := calendarCreateForm()
		values.Set("reminder", value)
		draft, err := parseCalendarEventDraft(calendarCreateHTTPRequest(values, "one"))
		got := "unset"
		if draft.ReminderSet {
			got = "none"
			if draft.ReminderMinutes != nil {
				got = itoa(*draft.ReminderMinutes)
			}
		}
		if err != nil || got != want {
			t.Errorf("%s: %s %v", value, got, err)
		}
	}
	for _, value := range []string{"-5", "soon", "999999"} {
		values := calendarCreateForm()
		values.Set("reminder", value)
		if _, err := parseCalendarEventDraft(calendarCreateHTTPRequest(values, "one")); err == nil {
			t.Errorf("invalid reminder %q accepted", value)
		}
	}
}

// Google gets one pop-up reminder and keeps its email ones; Outlook one reminder.
func TestCalendarReminderPayloads(t *testing.T) {
	thirty := 30
	payload := googleCalendarReminderPayload(&thirty, []googleCalendarReminder{{Method: "email", Minutes: 60}, {Method: "popup", Minutes: 10}})
	raw, _ := json.Marshal(payload)
	if string(raw) != `{"overrides":[{"method":"email","minutes":60},{"method":"popup","minutes":30}],"useDefault":false}` {
		t.Fatalf("Google payload %s", raw)
	}
	outlook := map[string]any{}
	outlookCalendarReminderPayload(outlook, nil)
	if outlook["isReminderOn"] != false || outlook["reminderMinutesBeforeStart"] != nil {
		t.Fatalf("Outlook payload %v", outlook)
	}
	draft := calendarProviderDraft(t, false)
	draft.ReminderSet, draft.ReminderMinutes = true, &thirty
	body, err := calendarCreateICS(draft)
	if err != nil || !strings.Contains(body, "BEGIN:VALARM") || !strings.Contains(body, "TRIGGER:-PT30M") {
		t.Fatalf("CalDAV create: %v %s", err, body)
	}
	if !calendarSameReminder(&thirty, &thirty) || calendarSameReminder(&thirty, nil) || !calendarSameReminder(nil, nil) {
		t.Fatal("reminder comparison")
	}
}

// A due reminder is published once, for its owner and the calendar's account.
func TestCalendarReminderTickPublishesOnce(t *testing.T) {
	h := calendarWorkerFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(10*time.Minute), now.Add(70*time.Minute)
	ten, sixty := 10, 60
	events := []storage.CalendarEvent{
		{ID: "due", RemoteID: "due", ETag: `"v1"`, Summary: "Planning", Location: "Room 2", StartAt: &start, EndAt: &end, ReminderMinutes: &ten},
		{ID: "later", RemoteID: "later", ETag: `"v1"`, Summary: "Later", StartAt: &end, EndAt: &end, ReminderMinutes: &ten},
		{ID: "past", RemoteID: "past", ETag: `"v1"`, Summary: "Past", StartAt: &start, EndAt: &end, ReminderMinutes: &sixty},
		{ID: "declined", RemoteID: "declined", ETag: `"v1"`, Summary: "Declined", StartAt: &start, EndAt: &end, ReminderMinutes: &ten, ResponseStatus: "declined"},
	}
	if err := h.db.ReplaceCalendarEvents(t.Context(), "one", "one-source", events, now.Add(-2*time.Hour), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	subscription := h.syncer.Events().SubscribeFor(mail.SubscribeOptions{All: true, Types: []mail.EventType{mail.EventCalendarReminder}})
	defer subscription.Close()
	h.runCalendarReminderTick(t.Context(), now.Add(30*time.Second))
	h.runCalendarReminderTick(t.Context(), now.Add(31*time.Second))
	var got []mail.Event
	deadline := time.After(2 * time.Second)
	for len(got) < 1 {
		select {
		case <-subscription.Ready():
			got = append(got, subscription.Take()...)
		case <-deadline:
			t.Fatal("no reminder published")
		}
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case <-subscription.Ready():
		got = append(got, subscription.Take()...)
	default:
	}
	if len(got) != 1 || got[0].UserID != "one" || got[0].AccountID != "one-account" || got[0].Payload["event_id"] != "due" || got[0].Payload["title"] != "Planning" || !strings.HasPrefix(got[0].Payload["body"].(string), "In 10 minutes · ") || !strings.HasSuffix(got[0].Payload["body"].(string), " · Room 2") {
		t.Fatalf("reminders published: %+v", got)
	}
}
