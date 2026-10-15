package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func TestOutlookWindowsZonesLoad(t *testing.T) {
	for windows, name := range outlookWindowsZones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("%s: %s does not load: %v", windows, name, err)
		}
	}
	if outlookCalendarTimeLocation("GMT Standard Time").String() != "Europe/London" {
		t.Fatal("GMT Standard Time is London, with daylight saving")
	}
}

// Microsoft reads times in UTC; the zone the event was made in is kept.
func TestOutlookEventKeepsItsOriginalZone(t *testing.T) {
	for original, want := range map[string]string{"Central Europe Standard Time": "Europe/Budapest", "Europe/Prague": "Europe/Prague", "UTC": "UTC", "": "UTC", "Customized Time Zone": "UTC"} {
		var outlook outlookCalendarEvent
		_ = json.Unmarshal([]byte(`{"id":"o","start":{"dateTime":"2026-11-03T08:00:00","timeZone":"UTC"},"end":{"dateTime":"2026-11-03T09:00:00","timeZone":"UTC"},"originalStartTimeZone":"`+original+`","originalEndTimeZone":"`+original+`"}`), &outlook)
		event, err := normalizeOutlookCalendarEvent(outlook)
		if err != nil || event.StartTimeZone != want || event.EndTimeZone != want || !event.StartAt.Equal(time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)) {
			t.Errorf("%q: %s %s %v %v", original, event.StartTimeZone, event.EndTimeZone, event.StartAt, err)
		}
	}
}

// Gofer's own CalDAV events keep their times in UTC and the zone beside them.
func TestCalDAVEventKeepsGoferZone(t *testing.T) {
	for extra, want := range map[string]string{"X-GOFER-TIMEZONE:Europe/Prague\r\n": "Europe/Prague", "X-GOFER-TIMEZONE:Nowhere/Else\r\n": "UTC", "": "UTC"} {
		cal, err := calendarUpdateDecodeICS([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:c\r\nDTSTART:20261103T080000Z\r\nDTEND:20261103T090000Z\r\n" + extra + "END:VEVENT\r\nEND:VCALENDAR\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		event, err := normalizeCalDAVEvent(cal.Events()[0], "c.ics", `"v1"`, time.UTC)
		if err != nil || event.StartTimeZone != want || !event.StartAt.Equal(time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)) {
			t.Errorf("%q: %s %v", extra, event.StartTimeZone, err)
		}
	}
	// Another zone in DTSTART wins over Gofer's note.
	cal, _ := calendarUpdateDecodeICS([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:c\r\nDTSTART;TZID=America/New_York:20261103T080000\r\nDTEND;TZID=America/New_York:20261103T090000\r\nX-GOFER-TIMEZONE:Europe/Prague\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	if event, err := normalizeCalDAVEvent(cal.Events()[0], "c.ics", `"v1"`, time.UTC); err != nil || event.StartTimeZone != "America/New_York" {
		t.Fatalf("DTSTART zone: %s %v", event.StartTimeZone, err)
	}
}

// A single event in UTC opens in the user's zone; a series keeps UTC.
func TestCalendarEditorShowsUTCEventsInTheUsersZone(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	start, end := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC), time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	event := storage.CalendarEvent{ID: "e", ETag: `"v1"`, Summary: "Review", StartAt: &start, EndAt: &end, StartTimeZone: "UTC", EndTimeZone: "UTC"}
	source := storage.CalendarSource{ID: "s", Provider: "caldav"}
	data, err := calendarEditData(event, source, "", prague, false, false, nil)
	if err != nil || data.TimeZone != "Europe/Prague" || data.StartTime != "09:00" || data.EndTime != "10:00" {
		t.Fatalf("single event: %s %s-%s %v", data.TimeZone, data.StartTime, data.EndTime, err)
	}
	data, err = calendarEditData(event, source, "", prague, false, true, nil)
	if err != nil || data.TimeZone != "UTC" || data.StartTime != "08:00" {
		t.Fatalf("date of a series: %s %s %v", data.TimeZone, data.StartTime, err)
	}
	event.StartTimeZone = "America/New_York"
	if data, _ := calendarEditData(event, source, "", prague, false, false, nil); data.TimeZone != "America/New_York" || data.StartTime != "03:00" {
		t.Fatalf("event zone: %s %s", data.TimeZone, data.StartTime)
	}
}
