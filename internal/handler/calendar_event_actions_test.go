package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/auth"
)

func calendarEventActionsRequest(t *testing.T, h *Handler, user string) (*httptest.ResponseRecorder, calendarEventActions) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/calendar/events/edit-event/actions", nil)
	r.SetPathValue("id", "edit-event")
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{ID: user}))
	w := httptest.NewRecorder()
	h.handleCalendarEventActions(w, r)
	var actions calendarEventActions
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &actions); err != nil {
			t.Fatalf("decode actions: %v", err)
		}
	}
	return w, actions
}

// The right-click menu asks what an event allows; the answer matches what
// its details dialog offers.
func TestCalendarEventActionsMatchTheDetailsDialog(t *testing.T) {
	h := calendarDeleteFixture(t)
	w, own := calendarEventActionsRequest(t, h, "one")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("actions status = %d, headers = %v", w.Code, w.Header())
	}
	if !own.CanEdit || !own.CanDelete || own.DeleteVersion != `"v1"` || own.HasOccurrence || own.EditSeries || own.DeleteSeries || own.Response != nil || own.JoinURL != "" {
		t.Fatalf("own event actions = %#v, want editable and deletable at version v1", own)
	}

	if _, err := h.db.Write().Exec(`UPDATE calendar_sources SET access_role='reader' WHERE id='one-source'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Write().Exec(`UPDATE calendar_events SET description=?,online_meeting_json='{}',response_status='needsAction',organizer_email='organizer@example.com',attendees_json='[{"email":"one@example.com"}]' WHERE id='edit-event'`, originalOnlineBody); err != nil {
		t.Fatal(err)
	}
	_, readOnly := calendarEventActionsRequest(t, h, "one")
	if readOnly.CanEdit || readOnly.CanDelete || readOnly.JoinURL == "" {
		t.Fatalf("read-only meeting actions = %#v, want a join link but no editing or deleting", readOnly)
	}

	_, invited := calendarEventActionsRequest(t, calendarResponseFixture(t, false), "one")
	if invited.Response == nil || invited.Response.Status != "needsAction" || invited.Response.Version == "" {
		t.Fatalf("invitation actions = %#v, want an unanswered reply with its version", invited)
	}

	if w, _ := calendarEventActionsRequest(t, h, "someone-else"); w.Code != http.StatusNotFound {
		t.Fatalf("another user's actions status = %d, want 404", w.Code)
	}
}
