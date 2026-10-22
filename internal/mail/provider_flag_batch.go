package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// ProviderRequest sends one authenticated JSON request to a provider API and
// decodes the answer into out, as each worker does for single changes.
type ProviderRequest func(method, endpoint string, body, out any) error

// ProviderFlagMessage is one message of a batched read or starred change:
// its provider id first, then the ids of its further copies in the folder.
type ProviderFlagMessage struct {
	IDs []string
}

// ProviderStatusError gives err the HTTP status a provider answered with,
// for callers whose request helpers report statuses their own way.
func ProviderStatusError(status int, err error) error {
	if err == nil || status == 0 {
		return err
	}
	return &providerAPIError{StatusCode: status, Body: err.Error()}
}

// ErrFlagBatchRefused marks messages a batch cannot carry: those of a Gmail
// call the API turned down as a whole, as it does when one id no longer
// exists, and an Outlook message with more copies than one $batch takes.
// They go one by one.
var ErrFlagBatchRefused = errors.New("provider refused the batched change")

const (
	// gmailFlagBatchIDs is the most ids one messages.batchModify takes.
	gmailFlagBatchIDs = 1000
	// outlookFlagBatchRequests is the most requests one Graph $batch takes.
	outlookFlagBatchRequests = 20
)

// SendGmailFlagBatch sets or clears read or starred on many Gmail messages
// with messages.batchModify, up to a thousand ids a call. Gmail answers for
// a call as a whole, so failures[i] is that of the call messages[i] was in;
// a call refused as malformed gives ErrFlagBatchRefused.
func SendGmailFlagBatch(request ProviderRequest, baseURL, kind string, target bool, messages []ProviderFlagMessage) []error {
	label, add := "UNREAD", !target
	if kind == storage.MessageMutationStarred {
		label, add = "STARRED", target
	}
	key := "removeLabelIds"
	if add {
		key = "addLabelIds"
	}
	failures := make([]error, len(messages))
	send := func(first, end int, ids []string) {
		err := request(http.MethodPost, baseURL+"/users/me/messages/batchModify", map[string][]string{"ids": ids, key: {label}}, nil)
		if status, ok := providerAPIStatus(err); ok && (status == http.StatusBadRequest || status == http.StatusNotFound) {
			err = fmt.Errorf("%w: %w", ErrFlagBatchRefused, err)
		}
		for i := first; i < end; i++ {
			failures[i] = err
		}
	}
	var ids []string
	first := 0
	for i, message := range messages {
		if len(ids) > 0 && len(ids)+len(message.IDs) > gmailFlagBatchIDs {
			send(first, i, ids)
			ids, first = nil, i
		}
		ids = append(ids, message.IDs...)
	}
	if len(ids) > 0 {
		send(first, len(messages), ids)
	}
	return failures
}

type graphBatchRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
}

type graphBatchResponse struct {
	Responses []struct {
		ID     string          `json:"id"`
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"responses"`
}

// SendOutlookFlagBatch sets or clears read or flagged on many Outlook
// messages through Graph's $batch, twenty updates a call, with immutable
// ids as single updates use. Each message gets its own result; a copy Graph
// no longer has counts as done.
func SendOutlookFlagBatch(request ProviderRequest, baseURL, kind string, target bool, messages []ProviderFlagMessage) []error {
	var payload any = map[string]bool{"isRead": target}
	if kind == storage.MessageMutationStarred {
		status := "notFlagged"
		if target {
			status = "flagged"
		}
		payload = map[string]any{"flag": map[string]string{"flagStatus": status}}
	}
	type part struct{ message int }
	failures := make([]error, len(messages))
	var requests []graphBatchRequest
	var parts []part
	send := func() {
		if len(requests) == 0 {
			return
		}
		var answer graphBatchResponse
		err := request(http.MethodPost, baseURL+"/$batch", map[string]any{"requests": requests}, &answer)
		answered := make(map[string]bool, len(answer.Responses))
		for _, response := range answer.Responses {
			index, convErr := strconv.Atoi(response.ID)
			if err != nil || convErr != nil || index < 0 || index >= len(parts) {
				continue
			}
			answered[response.ID] = true
			if response.Status == http.StatusNotFound || (response.Status >= 200 && response.Status < 300) {
				continue
			}
			if message := parts[index].message; failures[message] == nil {
				failures[message] = &providerAPIError{StatusCode: response.Status, Body: string(response.Body)}
			}
		}
		for index, p := range parts {
			if failures[p.message] != nil || answered[strconv.Itoa(index)] {
				continue
			}
			if err == nil {
				err = errors.New("Graph $batch gave no answer for an update")
			}
			failures[p.message] = err
		}
		requests, parts = nil, nil
	}
	for i, message := range messages {
		if len(message.IDs) > outlookFlagBatchRequests {
			failures[i] = ErrFlagBatchRefused
			continue
		}
		// A message's updates travel in one call, so it succeeds or fails whole.
		if len(requests) > 0 && len(requests)+len(message.IDs) > outlookFlagBatchRequests {
			send()
		}
		for _, id := range message.IDs {
			requests = append(requests, graphBatchRequest{
				ID:      strconv.Itoa(len(requests)),
				Method:  http.MethodPatch,
				URL:     "/me/messages/" + url.PathEscape(id),
				Headers: map[string]string{"Content-Type": "application/json", "Prefer": `IdType="ImmutableId"`},
				Body:    payload,
			})
			parts = append(parts, part{message: i})
		}
	}
	send()
	return failures
}
