package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"testing"
)

func calendarForwardRequest(values url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/calendar/events/e/forward", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestCalendarForwardForm(t *testing.T) {
	recipients, comment, err := parseCalendarForwardForm(calendarForwardRequest(url.Values{"recipients": {"Ada Field <ada@example.com>, bo@example.com\nada@example.com"}, "comment": {"  See you there  "}}))
	if err != nil || len(recipients) != 2 || recipients[0].Name != "Ada Field" || recipients[1].Address != "bo@example.com" || comment != "See you there" {
		t.Fatalf("%v %q %v", recipients, comment, err)
	}
	for _, values := range []url.Values{
		{"recipients": {""}, "comment": {""}},
		{"recipients": {"not an address"}, "comment": {""}},
		{"recipients": {"ada@example.com"}},
		{"recipients": {"ada@example.com"}, "comment": {""}, "extra": {"x"}},
	} {
		if _, _, err := parseCalendarForwardForm(calendarForwardRequest(values)); err == nil {
			t.Errorf("accepted %v", values)
		}
	}
}

// The forwarded invitation keeps the organizer and guests, invites the new
// people, and leaves out personal alarms.
func TestCalendarForwardICS(t *testing.T) {
	cal, err := calendarUpdateDecodeICS([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:planning\r\nDTSTAMP:20261001T080000Z\r\nDTSTART:20261103T080000Z\r\nDTEND:20261103T090000Z\r\nSUMMARY:Planning\r\nORGANIZER:mailto:host@example.com\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:guest@example.com\r\nX-PRIVATE:secret\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := calendarForwardICS(cal, []*mail.Address{{Name: "Ada Field", Address: "ada@example.com"}, {Address: "guest@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"METHOD:REQUEST", "ORGANIZER:mailto:host@example.com", "mailto:guest@example.com", "CN=Ada Field", "RSVP=TRUE", "mailto:ada@example.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if strings.Count(body, "mailto:guest@example.com") != 1 || strings.Contains(body, "VALARM") || strings.Contains(body, "X-PRIVATE") {
		t.Fatalf("wrong invitation:\n%s", body)
	}
}

func TestCalendarForwardOutlook(t *testing.T) {
	var got struct {
		ToRecipients []struct {
			EmailAddress struct{ Address, Name string }
		}
		Comment string
	}
	status := http.StatusAccepted
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		if r.Method != http.MethodPost || r.URL.Path != "/events/one/forward" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	target := calendarResponseTarget{Endpoint: server.URL + "/events/one", Token: "token"}
	if err := forwardOutlookInvitation(t.Context(), target, []*mail.Address{{Name: "Ada Field", Address: "ada@example.com"}}, "Join us"); err != nil {
		t.Fatal(err)
	}
	if len(got.ToRecipients) != 1 || got.ToRecipients[0].EmailAddress.Address != "ada@example.com" || got.ToRecipients[0].EmailAddress.Name != "Ada Field" || got.Comment != "Join us" {
		t.Fatalf("wrong payload %+v", got)
	}
	status = http.StatusForbidden
	if err := forwardOutlookInvitation(t.Context(), target, []*mail.Address{{Address: "ada@example.com"}}, ""); err == nil {
		t.Fatal("a refused forward succeeded")
	}
}
