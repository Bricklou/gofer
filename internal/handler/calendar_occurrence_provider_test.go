package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/calendar"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func TestCalendarJSONOccurrenceMutation(t *testing.T) {
	for _, graph := range []bool{false, true} {
		for _, deleting := range []bool{false, true} {
			for _, mode := range []string{"timed", "all-day", "exception", "wrong-parent", "master", "stale", "race", "guests", "organizer-guests", "guest-change", "invitation", "meeting", "unconfirmed", "wrong-result", "boundary"} {
				t.Run(fmt.Sprintf("graph=%v/delete=%v/%s", graph, deleting, mode), func(t *testing.T) {
					draft := calendarProviderDraft(t, mode == "all-day")
					existing := storage.CalendarEvent{RemoteID: "instance", SeriesRemoteID: "master", ETag: `"v1"`, Description: "Old notes"}
					current := map[string]any{"id": "instance", "etag": `"v1"`, "summary": "Before", "recurringEventId": "master",
						"originalStartTime": map[string]string{"dateTime": "2026-10-02T07:00:00Z"}, "start": map[string]string{"dateTime": "2026-10-02T07:00:00Z"}, "end": map[string]string{"dateTime": "2026-10-02T08:00:00Z"}}
					if mode == "all-day" {
						current["originalStartTime"] = map[string]string{"date": "2026-10-02"}
					}
					if graph {
						existing.ETag = "v1"
						current = map[string]any{"id": "instance", "changeKey": "v1", "@odata.etag": `W/"v1"`, "type": "occurrence", "seriesMasterId": "master", "subject": "Before",
							"start": map[string]string{"dateTime": "2026-10-02T07:00:00", "timeZone": "UTC"}, "end": map[string]string{"dateTime": "2026-10-02T08:00:00", "timeZone": "UTC"}}
						if mode == "exception" {
							current["type"] = "exception"
						}
					}
					switch mode {
					case "wrong-parent":
						current["seriesMasterId"], current["recurringEventId"] = "other", "other"
					case "master":
						current["type"], current["seriesMasterId"], current["recurringEventId"], current["originalStartTime"] = "seriesMaster", "", "", nil
					case "stale":
						current["etag"], current["changeKey"] = `"newer"`, "newer"
					case "guests":
						current["attendees"] = []any{map[string]string{"email": "guest@example.com"}}
					case "organizer-guests", "guest-change":
						// The organizer can change one date of a meeting, guests included.
						current["organizer"], current["isOrganizer"] = map[string]any{"self": true, "email": "owner@example.com", "emailAddress": map[string]string{"address": "owner@example.com"}}, true
						current["attendees"] = []any{map[string]any{"email": "guest@example.com", "emailAddress": map[string]string{"address": "guest@example.com"}, "type": "required"}}
						if mode == "guest-change" {
							draft.GuestsSet = true
							draft.Guests = []calendar.GuestDraft{{Email: "guest@example.com"}, {Email: "second@example.com"}}
						}
						existing.ResponseStatus, existing.AttendeesJSON = "organizer", `[{"email":"guest@example.com"}]`
					case "invitation":
						current["organizer"], current["isOrganizer"] = map[string]bool{"self": false}, false
					case "meeting":
						current["isOnlineMeeting"], current["hangoutLink"] = true, "https://meet.example"
					}
					reads, writes := 0, 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if !strings.HasSuffix(r.URL.Path, "/calendars/work/events/instance") || r.Header.Get("Authorization") != "Bearer token" {
							t.Error("wrong target or credentials")
						}
						if r.Method == http.MethodGet {
							reads++
							_ = json.NewEncoder(w).Encode(current)
							return
						}
						writes++
						etag := `"v1"`
						if graph {
							etag = `W/"v1"`
						}
						if reads != 1 || r.Header.Get("If-Match") != etag {
							t.Error("write lacked fresh preflight and conditional version")
						}
						if guests := mode == "organizer-guests" || mode == "guest-change"; !graph && guests != (r.URL.Query().Get("sendUpdates") == "all") {
							t.Errorf("guest notification mismatch: %s", r.URL.RawQuery)
						}
						if mode == "race" {
							w.WriteHeader(412)
							return
						}
						if mode == "boundary" && !deleting && graph {
							w.WriteHeader(400)
							_, _ = w.Write([]byte(`{"error":{"code":"ErrorOccurrenceCrossingBoundary"}}`))
							return
						}
						if deleting {
							if r.Method != http.MethodDelete {
								t.Error("wrong deletion method")
							}
							if mode == "unconfirmed" {
								w.WriteHeader(202)
							} else {
								w.WriteHeader(204)
							}
							return
						}
						if r.Method != http.MethodPatch {
							t.Error("wrong edit method")
						}
						var patch map[string]any
						_ = json.NewDecoder(r.Body).Decode(&patch)
						if _, exists := patch["attendees"]; exists != (mode == "guest-change") {
							t.Errorf("occurrence PATCH attendees mismatch: %v", patch["attendees"])
						}
						for _, field := range []string{"recurrence", "recurringEventId", "seriesMasterId", "originalStartTime", "reminders"} {
							if _, exists := patch[field]; exists {
								t.Errorf("occurrence PATCH changed %s", field)
							}
						}
						for key, value := range patch {
							current[key] = value
						}
						current["etag"], current["changeKey"], current["@odata.etag"] = `"v2"`, "v2", `W/"v2"`
						if graph {
							current["type"] = "exception"
						}
						if mode == "wrong-result" {
							current["seriesMasterId"], current["recurringEventId"] = "other", "other"
						}
						if mode == "unconfirmed" {
							_, _ = w.Write([]byte("invalid"))
							return
						}
						_ = json.NewEncoder(w).Encode(current)
					}))
					defer server.Close()
					oldGoogle, oldGraph := googleCalendarAPIBaseURL, outlookGraphBaseURL
					googleCalendarAPIBaseURL, outlookGraphBaseURL = server.URL, server.URL
					defer func() { googleCalendarAPIBaseURL, outlookGraphBaseURL = oldGoogle, oldGraph }()
					var remote calendar.RemoteEvent
					var err error
					if graph && deleting {
						err = deleteOutlookCalendarEventScope(t.Context(), "token", "work", existing, false, true)
					} else if graph {
						remote, err = updateOutlookCalendarEventScope(t.Context(), "token", "work", existing, draft, false, true)
					} else if deleting {
						err = deleteGoogleCalendarEventScope(t.Context(), "token", "work", existing, false, true)
					} else {
						remote, err = updateGoogleCalendarEventScope(t.Context(), "token", "work", existing, draft, false, true)
					}
					switch mode {
					case "stale", "race":
						if !errors.Is(err, errCalendarUpdateConflict) || mode == "stale" && writes != 0 {
							t.Fatalf("conflict: %d writes, %v", writes, err)
						}
					case "wrong-parent", "master", "guests", "invitation", "meeting":
						if !errors.Is(err, errCalendarUpdateUnsupported) || writes != 0 {
							t.Fatalf("unsafe write: %d writes, %v", writes, err)
						}
					case "unconfirmed":
						if err == nil || writes != 1 {
							t.Fatal("accepted unconfirmed write")
						}
					case "wrong-result":
						if !deleting && err == nil {
							t.Fatal("accepted different series after save")
						}
					case "boundary":
						if !deleting && graph && !errors.Is(err, errCalendarOccurrenceBoundary) {
							t.Fatalf("missing Outlook boundary explanation: %v", err)
						}
					default:
						if err != nil || writes != 1 {
							t.Fatalf("mutation: writes=%d, %v", writes, err)
						}
						if !deleting && (remote.RemoteID != existing.RemoteID || remote.SeriesRemoteID != "master" || !calendarUpdateMatchesDraft(remote, draft)) {
							t.Fatalf("wrong saved occurrence: %+v", remote)
						}
						if mode == "guest-change" && !deleting && !calendarMeetingGuestsMatch(remote.Attendees, draft.Guests, "owner@example.com") {
							t.Fatalf("occurrence guests not saved: %s", remote.Attendees)
						}
					}
				})
			}
		}
	}
}
