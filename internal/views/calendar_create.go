package views

import (
	"encoding/json"
	"github.com/cristianadrielbraun/gofer/internal/calendar"
	"strconv"
	"time"
)

func calendarTeamsEventParams(data CalendarCreateData) string {
	scope := ""
	if data.EditSeries {
		scope = "series"
	} else if data.EditOccurrence {
		scope = "occurrence"
	}
	raw, _ := json.Marshal(map[string]string{"event_id": data.EventID, "edit_scope": scope})
	return string(raw)
}

func calendarCreatePickerValue(layout, value string) time.Time {
	at, _ := time.Parse(layout, value)
	return at
}

type CalendarCreateSource struct {
	ID          string
	Name        string
	AccountName string
	Writable    bool
	Authorized  bool
	Provider    string
}

type CalendarCreateData struct {
	RequestID       string
	EventID         string
	Version         string
	EditSeries      bool
	EditOccurrence  bool
	Recurrence      *calendar.RecurrenceDraft
	Summary         string
	Description     string
	DescriptionHTML string
	Location        string
	AllDay          bool
	SourceID        string
	TeamsState      string
	MeetingJoinURL  string
	// TeamsReplaceDelete is the delete request that removes this Teams meeting
	// once a copy without Teams is created. Empty when it can't be replaced.
	TeamsReplaceDelete string
	// Reminder is the event's reminder in minutes, "none", or empty for a new
	// event, which keeps the calendar's default.
	Reminder string
	// ShowAs is how the event marks its time; empty means busy.
	ShowAs    string
	Private   bool
	Sources   []CalendarCreateSource
	Date      string
	StartTime string
	// EndDate is inclusive for all-day form values, including edit prefill.
	EndDate  string
	EndTime  string
	TimeZone string
	Guests   string
}

type CalendarTeamsData struct {
	State, SourceID, Provider, JoinURL, EventID string
	// Replaceable: an existing Teams meeting that can be replaced by a copy
	// without Teams, since Microsoft can't turn Teams off. Series: that copy
	// is the whole series.
	Replaceable, Series bool
}

// calendarDialogClass sizes a calendar dialog as a bounded column: header and
// footer stay put while the body scrolls inside the viewport. Let content grow
// naturally up to the screen height instead of imposing a fixed desktop cap.
func calendarDialogClass(width string) string {
	return "calendar-dialog w-[calc(100vw_-_2rem)] max-w-[calc(100vw_-_2rem)] " + width +
		" max-h-[calc(100dvh-2rem)] [&_[data-tui-dialog-panel]]:flex [&_[data-tui-dialog-panel]]:max-h-[calc(100dvh-2rem)]" +
		" [&_[data-tui-dialog-panel]]:min-h-0 [&_[data-tui-dialog-panel]]:flex-col [&_[data-tui-dialog-panel]]:gap-0 [&_[data-tui-dialog-panel]]:p-0"
}

// calendarReminderChoices lists the editor's reminder options, keeping an
// event's own reminder when it isn't one of them.
func calendarReminderChoices(data CalendarCreateData) (string, [][2]string) {
	choices := [][2]string{{"none", "No reminder"}, {"0", "At the start"}, {"5", "5 minutes before"}, {"10", "10 minutes before"}, {"15", "15 minutes before"}, {"30", "30 minutes before"}, {"60", "1 hour before"}, {"1440", "1 day before"}}
	current := data.Reminder
	if current == "" {
		return "default", append([][2]string{{"default", "Calendar's default reminder"}}, choices...)
	}
	for _, choice := range choices {
		if choice[0] == current {
			return current, choices
		}
	}
	if minutes, err := strconv.Atoi(current); err == nil {
		return current, append(choices, [2]string{current, CalendarReminderLabel(minutes)})
	}
	return "none", choices
}

// CalendarReminderLabel describes a reminder, in minutes before the start.
func CalendarReminderLabel(minutes int) string {
	if minutes == 0 {
		return "At the start"
	}
	return CalendarReminderSpan(minutes) + " before"
}

// CalendarReminderSpan says how long a reminder is, such as "15 minutes".
func CalendarReminderSpan(minutes int) string {
	switch {
	case minutes > 0 && minutes%1440 == 0:
		return plural(minutes/1440, "day")
	case minutes > 0 && minutes%60 == 0:
		return plural(minutes/60, "hour")
	}
	return plural(minutes, "minute")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// calendarShowAsChoices lists busy and free, keeping an Outlook event's other
// state when it has one.
func calendarShowAsChoices(data CalendarCreateData) (string, [][2]string) {
	choices := [][2]string{{"busy", "Busy"}, {"free", "Free"}}
	switch data.ShowAs {
	case "", "busy":
		return "busy", choices
	case "free":
		return "free", choices
	}
	return data.ShowAs, append(choices, [2]string{data.ShowAs, CalendarShowAsLabel(data.ShowAs)})
}

// CalendarShowAsLabel names how an event marks its time.
func CalendarShowAsLabel(showAs string) string {
	switch showAs {
	case "free":
		return "Free"
	case "tentative":
		return "Tentative"
	case "oof":
		return "Away"
	case "working_elsewhere":
		return "Working elsewhere"
	}
	return "Busy"
}

func calendarPrivateValue(data CalendarCreateData) string {
	if data.Private {
		return "true"
	}
	return "false"
}
