package handler

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func readMbox(t *testing.T, data string, offset int64) []mboxMessage {
	t.Helper()
	reader := newMboxReader(strings.NewReader(data[offset:]), offset)
	var messages []mboxMessage
	for {
		msg, err := reader.Next(true)
		if err == io.EOF {
			return messages
		}
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
}

func TestMboxReaderSplitsMessagesAndUnquotesFromLines(t *testing.T) {
	data := "From juno@example.test Mon Mar  3 10:00:00 2025\n" +
		"Subject: One\n\nFirst body.\n>From the desk of Juno.\n>>From an older note.\nFrom inside a paragraph stays.\n\n" +
		"From robin@example.test Tue Mar  4 11:00:00 2025\r\n" +
		"Subject: Two\r\n\r\nSecond body.\r\n\r\n"
	messages := readMbox(t, data, 0)
	if len(messages) != 2 {
		t.Fatalf("read %d messages", len(messages))
	}
	if got := string(messages[0].Raw); got != "Subject: One\n\nFirst body.\nFrom the desk of Juno.\n>From an older note.\nFrom inside a paragraph stays.\n" {
		t.Fatalf("first message = %q", got)
	}
	if got := string(messages[1].Raw); got != "Subject: Two\r\n\r\nSecond body.\r\n" {
		t.Fatalf("second message = %q", got)
	}
	if messages[0].FromLine != "From juno@example.test Mon Mar  3 10:00:00 2025" {
		t.Fatalf("from line = %q", messages[0].FromLine)
	}
	// Reading again from where the second message starts gives the same
	// message, as an import continuing after a restart does.
	if messages[0].End != messages[1].Start {
		t.Fatalf("first ends at %d, second starts at %d", messages[0].End, messages[1].Start)
	}
	again := readMbox(t, data, messages[1].Start)
	if len(again) != 1 || !bytes.Equal(again[0].Raw, messages[1].Raw) || again[0].End != int64(len(data)) {
		t.Fatalf("continuing read %d messages", len(again))
	}
}

func TestMboxReaderReadsAFileWithoutFromLineAsOneMessage(t *testing.T) {
	data := "Subject: Alone\r\n\r\nJust one message.\r\n"
	messages := readMbox(t, data, 0)
	if len(messages) != 1 || string(messages[0].Raw) != data {
		t.Fatalf("messages = %+v", messages)
	}
	if count, err := countMbox(strings.NewReader("")); err != nil || count != 0 {
		t.Fatalf("empty file has %d messages (%v)", count, err)
	}
}

func TestPrepareMailImportMessageReadsAndRemovesStateHeaders(t *testing.T) {
	raw := "From: Juno Tester <juno@example.test>\r\n" +
		"Subject: Garden\r\n" +
		"Status: RO\r\n" +
		"X-Status: AF\r\n" +
		"X-Mozilla-Keys: \r\n" +
		"  $label1\r\n" +
		"Message-ID: <Garden-1@Example.test>\r\n" +
		"\r\n" +
		"Status: this line is in the body\r\n"
	msg := prepareMailImportMessage([]byte(raw), "From juno@example.test Mon Mar  3 10:00:00 2025")
	if !msg.Read || !msg.Flagged || !msg.Answered || msg.Draft {
		t.Fatalf("state = read %t flagged %t answered %t draft %t", msg.Read, msg.Flagged, msg.Answered, msg.Draft)
	}
	want := "From: Juno Tester <juno@example.test>\r\nSubject: Garden\r\nMessage-ID: <Garden-1@Example.test>\r\n\r\nStatus: this line is in the body\r\n"
	if string(msg.Raw) != want {
		t.Fatalf("message = %q", msg.Raw)
	}
	if msg.MessageID != "Garden-1@Example.test" || msg.Subject != "Garden" {
		t.Fatalf("message ID %q, subject %q", msg.MessageID, msg.Subject)
	}
	if want := time.Date(2025, 3, 3, 10, 0, 0, 0, time.UTC); !msg.Date.Equal(want) {
		t.Fatalf("date = %v, want the From line's", msg.Date)
	}

	mozilla := prepareMailImportMessage([]byte("X-Mozilla-Status: 0005\nSubject: Thunderbird\n\nBody\n"), "")
	if !mozilla.Read || !mozilla.Flagged || mozilla.Answered {
		t.Fatalf("Thunderbird state = %+v", mozilla)
	}
	takeout := prepareMailImportMessage([]byte("X-Gmail-Labels: Inbox,Unread,Starred\nDate: Tue, 04 Mar 2025 11:00:00 +0100\n\nBody\n"), "")
	if takeout.Read || !takeout.Flagged || !strings.Contains(string(takeout.Raw), "X-Gmail-Labels") {
		t.Fatalf("Takeout state = %+v", takeout)
	}
	if want := time.Date(2025, 3, 4, 10, 0, 0, 0, time.UTC); !takeout.Date.Equal(want) {
		t.Fatalf("date = %v, want the Date header's", takeout.Date)
	}
}

func TestGuessMailImportRole(t *testing.T) {
	for name, want := range map[string]string{
		"Inbox": "inbox", "Sent Items": "sent", "Archive/2024": "", "Work/Junk E-mail": "junk", "Deleted Messages": "trash", "Clients": "",
	} {
		if got := guessMailImportRole(name); got != want {
			t.Errorf("role of %q = %q, want %q", name, got, want)
		}
	}
}
