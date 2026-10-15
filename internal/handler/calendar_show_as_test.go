package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Each provider says whether an event leaves the time free.
func TestCalendarShowAsReadFromEachProvider(t *testing.T) {
	for raw, want := range map[string]string{`"transparency":"transparent"`: "free", `"transparency":"opaque"`: "busy", `"status":"confirmed"`: "busy"} {
		var google googleCalendarEvent
		_ = json.Unmarshal([]byte(`{"id":"g","start":{"dateTime":"2026-11-03T09:00:00Z"},"end":{"dateTime":"2026-11-03T10:00:00Z"},`+raw+`}`), &google)
		if event, err := normalizeGoogleCalendarEvent(google); err != nil || event.ShowAs != want {
			t.Errorf("Google %s: %q %v", raw, event.ShowAs, err)
		}
	}
	for value, want := range map[string]string{"free": "free", "oof": "oof", "workingElsewhere": "working_elsewhere", "unknown": ""} {
		var outlook outlookCalendarEvent
		_ = json.Unmarshal([]byte(`{"id":"o","start":{"dateTime":"2026-11-03T09:00:00","timeZone":"UTC"},"end":{"dateTime":"2026-11-03T10:00:00","timeZone":"UTC"},"showAs":"`+value+`"}`), &outlook)
		if event, err := normalizeOutlookCalendarEvent(outlook); err != nil || event.ShowAs != want {
			t.Errorf("Outlook %s: %q %v", value, event.ShowAs, err)
		}
	}
	for line, want := range map[string]string{"TRANSP:TRANSPARENT\r\n": "free", "TRANSP:OPAQUE\r\n": "busy", "": "busy"} {
		cal, err := calendarUpdateDecodeICS([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:c\r\nDTSTART:20261103T090000Z\r\nDTEND:20261103T100000Z\r\n" + line + "END:VEVENT\r\nEND:VCALENDAR\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		if event, err := normalizeCalDAVEvent(cal.Events()[0], "c.ics", `"v1"`, time.UTC); err != nil || event.ShowAs != want {
			t.Errorf("CalDAV %q: %q %v", line, event.ShowAs, err)
		}
	}
}

func TestCalendarShowAsForm(t *testing.T) {
	for _, value := range []string{"busy", "free", "tentative", "oof", "working_elsewhere"} {
		values := calendarCreateForm()
		values.Set("show_as", value)
		if draft, err := parseCalendarEventDraft(calendarCreateHTTPRequest(values, "one")); err != nil || draft.ShowAs != value {
			t.Errorf("%s: %q %v", value, draft.ShowAs, err)
		}
	}
	for _, value := range []string{"", "away", "BUSY"} {
		values := calendarCreateForm()
		values.Set("show_as", value)
		if _, err := parseCalendarEventDraft(calendarCreateHTTPRequest(values, "one")); err == nil {
			t.Errorf("invalid show-as %q accepted", value)
		}
	}
}

// Google and CalDAV have only busy and free; Outlook keeps every state.
func TestCalendarShowAsPayloads(t *testing.T) {
	if googleCalendarTransparency("free") != "transparent" || googleCalendarTransparency("oof") != "opaque" {
		t.Fatal("Google transparency")
	}
	if outlookCalendarShowAsValue("working_elsewhere") != "workingElsewhere" || outlookCalendarShowAsValue("free") != "free" {
		t.Fatal("Outlook showAs")
	}
	draft := calendarProviderDraft(t, false)
	draft.ShowAs = "free"
	body, err := calendarCreateICS(draft)
	if err != nil || !strings.Contains(body, "TRANSP:TRANSPARENT") {
		t.Fatalf("CalDAV create: %v %s", err, body)
	}
	if !calendarSameShowAs("busy", "") || calendarSameShowAs("free", "") || !calendarSameShowAs("oof", "oof") {
		t.Fatal("show-as comparison")
	}
}
