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

// ProviderBatchMessage is one message of a batched change: its provider id
// first, then the ids of its further copies in the folder.
type ProviderBatchMessage struct {
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

// ErrBatchRefused marks messages a batch cannot carry: those of a Gmail call
// the API turned down as a whole, as it does when one id no longer exists,
// and an Outlook message with more copies than one $batch takes. They go one
// by one.
var ErrBatchRefused = errors.New("provider refused the batched change")

const (
	// gmailBatchIDs is the most ids one batchModify or batchDelete takes.
	gmailBatchIDs = 1000
	// outlookBatchRequests is the most requests one Graph $batch takes.
	outlookBatchRequests = 20
)

// SendGmailFlagBatch sets or clears read or starred on many Gmail messages
// with messages.batchModify, up to a thousand ids a call. Gmail answers for
// a call as a whole, so failures[i] is that of the call messages[i] was in;
// a call refused as malformed gives ErrBatchRefused.
func SendGmailFlagBatch(request ProviderRequest, baseURL, kind string, target bool, messages []ProviderBatchMessage) []error {
	label, add := "UNREAD", !target
	if kind == storage.MessageMutationStarred {
		label, add = "STARRED", target
	}
	key := "removeLabelIds"
	if add {
		key = "addLabelIds"
	}
	return sendGmailBatch(request, baseURL+"/users/me/messages/batchModify", map[string][]string{key: {label}}, messages)
}

// SendGmailDeleteBatch deletes many Gmail messages for good with
// messages.batchDelete, as single deletes do one at a time, a thousand ids
// a call. Results are per call, as for SendGmailFlagBatch.
func SendGmailDeleteBatch(request ProviderRequest, baseURL string, messages []ProviderBatchMessage) []error {
	return sendGmailBatch(request, baseURL+"/users/me/messages/batchDelete", nil, messages)
}

func sendGmailBatch(request ProviderRequest, endpoint string, fields map[string][]string, messages []ProviderBatchMessage) []error {
	failures := make([]error, len(messages))
	send := func(first, end int, ids []string) {
		body := map[string][]string{"ids": ids}
		for key, value := range fields {
			body[key] = value
		}
		err := request(http.MethodPost, endpoint, body, nil)
		if status, ok := providerAPIStatus(err); ok && (status == http.StatusBadRequest || status == http.StatusNotFound) {
			err = fmt.Errorf("%w: %w", ErrBatchRefused, err)
		}
		for i := first; i < end; i++ {
			failures[i] = err
		}
	}
	var ids []string
	first := 0
	for i, message := range messages {
		if len(ids) > 0 && len(ids)+len(message.IDs) > gmailBatchIDs {
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
	Body    any               `json:"body,omitempty"`
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
func SendOutlookFlagBatch(request ProviderRequest, baseURL, kind string, target bool, messages []ProviderBatchMessage) []error {
	var payload any = map[string]bool{"isRead": target}
	if kind == storage.MessageMutationStarred {
		status := "notFlagged"
		if target {
			status = "flagged"
		}
		payload = map[string]any{"flag": map[string]string{"flagStatus": status}}
	}
	failures, _ := sendOutlookBatch(request, baseURL, http.MethodPatch, payload, messages)
	return failures
}

// SendOutlookDeleteBatch deletes many Outlook messages through Graph's
// $batch, as single deletes do one at a time. gone[i] is set when Graph no
// longer had any of messages[i]'s copies.
func SendOutlookDeleteBatch(request ProviderRequest, baseURL string, messages []ProviderBatchMessage) (failures []error, gone []bool) {
	return sendOutlookBatch(request, baseURL, http.MethodDelete, nil, messages)
}

func sendOutlookBatch(request ProviderRequest, baseURL, method string, payload any, messages []ProviderBatchMessage) (failures []error, gone []bool) {
	failures = make([]error, len(messages))
	gone = make([]bool, len(messages))
	found := make([]bool, len(messages))
	var requests []graphBatchRequest
	var owners []int
	send := func() {
		if len(requests) == 0 {
			return
		}
		var answer graphBatchResponse
		err := request(http.MethodPost, baseURL+"/$batch", map[string]any{"requests": requests}, &answer)
		answered := make(map[int]bool, len(answer.Responses))
		for _, response := range answer.Responses {
			index, convErr := strconv.Atoi(response.ID)
			if err != nil || convErr != nil || index < 0 || index >= len(owners) {
				continue
			}
			answered[index] = true
			message := owners[index]
			switch {
			case response.Status == http.StatusNotFound:
			case response.Status >= 200 && response.Status < 300:
				found[message] = true
			case failures[message] == nil:
				failures[message] = &providerAPIError{StatusCode: response.Status, Body: string(response.Body)}
			}
		}
		for index, message := range owners {
			if failures[message] != nil || answered[index] {
				continue
			}
			if err == nil {
				err = errors.New("Graph $batch gave no answer for a change")
			}
			failures[message] = err
		}
		requests, owners = nil, nil
	}
	headers := map[string]string{"Prefer": `IdType="ImmutableId"`}
	if payload != nil {
		headers["Content-Type"] = "application/json"
	}
	for i, message := range messages {
		if len(message.IDs) > outlookBatchRequests {
			failures[i] = ErrBatchRefused
			continue
		}
		// A message's changes travel in one call, so it succeeds or fails whole.
		if len(requests) > 0 && len(requests)+len(message.IDs) > outlookBatchRequests {
			send()
		}
		for _, id := range message.IDs {
			requests = append(requests, graphBatchRequest{
				ID:      strconv.Itoa(len(requests)),
				Method:  method,
				URL:     "/me/messages/" + url.PathEscape(id),
				Headers: headers,
				Body:    payload,
			})
			owners = append(owners, i)
		}
	}
	send()
	for i := range messages {
		gone[i] = failures[i] == nil && !found[i]
	}
	return failures, gone
}
