package handler

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	netmail "net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/message"
)

// mailImportMaxMessageBytes is the largest message an import uploads; larger
// ones are skipped, like the largest Gofer downloads.
const mailImportMaxMessageBytes = int(message.MaxDownloadBytes)

var errMailImportTooLarge = errors.New("larger than Gofer uploads")

// mboxReader reads the messages of an mbox file one at a time. It reads
// mboxrd, where lines starting with "From " inside a message are quoted with
// ">", and so also the older mboxo files most mail apps write. A file that
// doesn't start with a "From " line is read as one message.
type mboxReader struct {
	in *bufio.Reader
	// offset is how far into the file the reader is.
	offset int64
	// next is the "From " line of the next message, already read.
	next    []byte
	started bool
	single  bool
	done    bool
}

func newMboxReader(in io.Reader, offset int64) *mboxReader {
	return &mboxReader{in: bufio.NewReaderSize(in, 64<<10), offset: offset}
}

func (r *mboxReader) line() ([]byte, error) {
	line, err := r.in.ReadBytes('\n')
	r.offset += int64(len(line))
	if err == io.EOF && len(line) > 0 {
		err = nil
	}
	return line, err
}

func isMboxSeparator(line []byte) bool {
	return bytes.HasPrefix(line, []byte("From "))
}

// mboxMessage is one message read from an mbox file. Start is where its
// "From " line starts and End where the next one does, for continuing an
// import from there.
type mboxMessage struct {
	Raw      []byte
	FromLine string
	Start    int64
	End      int64
	TooLarge bool
}

// Next returns the next message, or io.EOF after the last one. With keep
// false the message is read but not kept, for counting messages.
func (r *mboxReader) Next(keep bool) (mboxMessage, error) {
	if r.done {
		return mboxMessage{}, io.EOF
	}
	if !r.started {
		r.started = true
		for {
			start := r.offset
			line, err := r.line()
			if err == io.EOF {
				r.done = true
				return mboxMessage{}, io.EOF
			}
			if err != nil {
				return mboxMessage{}, err
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if isMboxSeparator(line) {
				r.next = line
				r.offset = start + int64(len(line))
			} else {
				// Not an mbox: the whole file is one message.
				r.single = true
				r.next = nil
				return r.rest(line, start, keep)
			}
			break
		}
	}
	if r.next == nil {
		r.done = true
		return mboxMessage{}, io.EOF
	}
	msg := mboxMessage{FromLine: strings.TrimRight(string(r.next), "\r\n"), Start: r.offset - int64(len(r.next))}
	r.next = nil
	var body bytes.Buffer
	size := 0
	blank := false
	for {
		lineStart := r.offset
		line, err := r.line()
		if err == io.EOF {
			r.done = true
			break
		}
		if err != nil {
			return mboxMessage{}, err
		}
		if blank && isMboxSeparator(line) {
			r.next = line
			msg.End = lineStart
			break
		}
		blank = len(bytes.TrimRight(line, "\r\n")) == 0
		if quoted := bytes.TrimLeft(line, ">"); len(quoted) < len(line) && bytes.HasPrefix(quoted, []byte("From ")) {
			line = line[1:]
		}
		size += len(line)
		if size > mailImportMaxMessageBytes {
			msg.TooLarge = true
		}
		if keep && !msg.TooLarge {
			body.Write(line)
		}
	}
	if r.done {
		msg.End = r.offset
	}
	if msg.TooLarge {
		return msg, nil
	}
	// The blank line before the next "From " line separates messages.
	raw := body.Bytes()
	if bytes.HasSuffix(raw, []byte("\r\n\r\n")) {
		raw = raw[:len(raw)-2]
	} else if bytes.HasSuffix(raw, []byte("\n\n")) {
		raw = raw[:len(raw)-1]
	}
	msg.Raw = raw
	return msg, nil
}

func (r *mboxReader) rest(first []byte, start int64, keep bool) (mboxMessage, error) {
	msg := mboxMessage{Start: start}
	var body bytes.Buffer
	size := len(first)
	if keep {
		body.Write(first)
	}
	for {
		line, err := r.line()
		if err == io.EOF {
			break
		}
		if err != nil {
			return mboxMessage{}, err
		}
		size += len(line)
		if size > mailImportMaxMessageBytes {
			msg.TooLarge = true
		}
		if keep && !msg.TooLarge {
			body.Write(line)
		}
	}
	r.done = true
	msg.End = r.offset
	if !msg.TooLarge {
		msg.Raw = body.Bytes()
	}
	return msg, nil
}

// mailImportMessage is a message ready to upload: its original without the
// state headers mail apps add to mbox files, and that state.
type mailImportMessage struct {
	Raw       []byte
	MessageID string
	Subject   string
	Date      time.Time
	Read      bool
	Flagged   bool
	Answered  bool
	Draft     bool
}

// mboxStateHeaders are the headers mail apps write into mbox files to keep a
// message's state. They are read and then left out of the uploaded message.
var mboxStateHeaders = map[string]bool{
	"status": true, "x-status": true, "x-mozilla-status": true, "x-mozilla-status2": true, "x-mozilla-keys": true,
}

// prepareMailImportMessage reads a message's state from its headers and
// removes the headers that only held that state. fromLine is the mbox "From "
// line, for messages without a Date header.
func prepareMailImportMessage(raw []byte, fromLine string) mailImportMessage {
	msg := mailImportMessage{}
	headerEnd := len(raw)
	for i := 0; i < len(raw); {
		end := bytes.IndexByte(raw[i:], '\n')
		if end < 0 {
			break
		}
		if len(bytes.TrimRight(raw[i:i+end+1], "\r\n")) == 0 {
			headerEnd = i
			break
		}
		i += end + 1
	}
	var kept bytes.Buffer
	kept.Grow(len(raw))
	var state struct{ status, xStatus, mozilla, gmailLabels string }
	skipping := false
	for i := 0; i < headerEnd; {
		end := bytes.IndexByte(raw[i:headerEnd], '\n')
		line := raw[i:headerEnd]
		if end >= 0 {
			line = raw[i : i+end+1]
		}
		i += len(line)
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if !skipping {
				kept.Write(line)
			}
			continue
		}
		name, value, _ := strings.Cut(strings.TrimRight(string(line), "\r\n"), ":")
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		switch name {
		case "status":
			state.status = value
		case "x-status":
			state.xStatus = value
		case "x-mozilla-status":
			state.mozilla = value
		case "x-gmail-labels":
			state.gmailLabels = value
		}
		skipping = mboxStateHeaders[name]
		if !skipping {
			kept.Write(line)
		}
	}
	kept.Write(raw[headerEnd:])
	msg.Raw = kept.Bytes()

	msg.Read = strings.ContainsRune(state.status, 'R')
	msg.Flagged = strings.ContainsRune(state.xStatus, 'F')
	msg.Answered = strings.ContainsRune(state.xStatus, 'A')
	msg.Draft = strings.ContainsRune(state.xStatus, 'T')
	if flags, err := strconv.ParseUint(state.mozilla, 16, 32); err == nil {
		msg.Read = msg.Read || flags&0x0001 != 0
		msg.Answered = msg.Answered || flags&0x0002 != 0
		msg.Flagged = msg.Flagged || flags&0x0004 != 0
	}
	if state.gmailLabels != "" {
		// Google Takeout marks unread messages with an Unread label.
		labels := map[string]bool{}
		for _, label := range strings.Split(state.gmailLabels, ",") {
			labels[strings.ToLower(strings.TrimSpace(label))] = true
		}
		msg.Read = !labels["unread"]
		msg.Flagged = msg.Flagged || labels["starred"]
	}

	if header, err := netmail.ReadMessage(bytes.NewReader(msg.Raw)); err == nil {
		msg.MessageID = message.NormalizeMessageID(header.Header.Get("Message-ID"))
		msg.Subject = message.DecodeHeader(header.Header.Get("Subject"))
		if date, err := header.Header.Date(); err == nil {
			msg.Date = date
		}
	}
	if msg.Date.IsZero() {
		msg.Date = mboxFromLineDate(fromLine)
	}
	return msg
}

// mboxFromLineDate reads the date at the end of an mbox "From " line.
func mboxFromLineDate(line string) time.Time {
	fields := strings.Fields(line)
	if len(fields) < 7 {
		return time.Time{}
	}
	value := strings.Join(fields[len(fields)-5:], " ")
	if date, err := time.Parse("Mon Jan _2 15:04:05 2006", value); err == nil {
		return date
	}
	return time.Time{}
}
