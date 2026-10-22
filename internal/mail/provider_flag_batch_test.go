package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func flagMessages(n int) []ProviderFlagMessage {
	messages := make([]ProviderFlagMessage, n)
	for i := range messages {
		messages[i] = ProviderFlagMessage{IDs: []string{fmt.Sprintf("m%d", i)}}
	}
	return messages
}

func TestSendGmailFlagBatchSplitsAtAThousandIDs(t *testing.T) {
	var calls []map[string][]string
	request := func(method, endpoint string, body, _ any) error {
		if method != http.MethodPost || endpoint != "https://gmail.test/users/me/messages/batchModify" {
			t.Fatalf("request %s %s", method, endpoint)
		}
		calls = append(calls, body.(map[string][]string))
		return nil
	}
	failures := SendGmailFlagBatch(request, "https://gmail.test", storage.MessageMutationRead, true, flagMessages(1500))
	if len(calls) != 2 || len(calls[0]["ids"]) != 1000 || len(calls[1]["ids"]) != 500 {
		t.Fatalf("batchModify calls = %d with %d and %d ids, want 1000 then 500", len(calls), len(calls[0]["ids"]), len(calls[len(calls)-1]["ids"]))
	}
	if got := calls[0]["removeLabelIds"]; len(got) != 1 || got[0] != "UNREAD" {
		t.Fatalf("marking read sends %#v, want UNREAD removed", calls[0])
	}
	for i, err := range failures {
		if err != nil {
			t.Fatalf("failures[%d] = %v", i, err)
		}
	}

	calls = nil
	SendGmailFlagBatch(request, "https://gmail.test", storage.MessageMutationStarred, true, flagMessages(1))
	if got := calls[0]["addLabelIds"]; len(got) != 1 || got[0] != "STARRED" {
		t.Fatalf("starring sends %#v, want STARRED added", calls[0])
	}
}

func TestSendGmailFlagBatchSendsRefusedBatchesOneByOne(t *testing.T) {
	refused := func(string, string, any, any) error {
		return &providerAPIError{StatusCode: http.StatusBadRequest, Body: "Invalid id value"}
	}
	for i, err := range SendGmailFlagBatch(refused, "https://gmail.test", storage.MessageMutationRead, true, flagMessages(3)) {
		if !errors.Is(err, ErrFlagBatchRefused) {
			t.Fatalf("failures[%d] = %v, want ErrFlagBatchRefused", i, err)
		}
	}
	throttled := func(string, string, any, any) error {
		return &providerAPIError{StatusCode: http.StatusTooManyRequests}
	}
	for i, err := range SendGmailFlagBatch(throttled, "https://gmail.test", storage.MessageMutationRead, true, flagMessages(3)) {
		if err == nil || errors.Is(err, ErrFlagBatchRefused) {
			t.Fatalf("failures[%d] = %v, want a retryable error, not one-by-one", i, err)
		}
	}
}

func TestSendOutlookFlagBatchGivesEachMessageItsOwnResult(t *testing.T) {
	// 19 single messages, then one with a copy, which must not be split
	// across calls, then one with more copies than a $batch takes.
	messages := flagMessages(19)
	messages = append(messages, ProviderFlagMessage{IDs: []string{"pair-a", "pair-b"}})
	oversized := ProviderFlagMessage{}
	for i := range outlookFlagBatchRequests + 1 {
		oversized.IDs = append(oversized.IDs, fmt.Sprintf("copy%d", i))
	}
	messages = append(messages, oversized)
	answers := map[string]int{"m3": http.StatusNotFound, "m5": http.StatusInternalServerError, "pair-b": http.StatusTooManyRequests}

	var sizes []int
	request := func(method, endpoint string, body, out any) error {
		if method != http.MethodPost || endpoint != "https://graph.test/v1.0/$batch" {
			t.Fatalf("request %s %s", method, endpoint)
		}
		raw, _ := json.Marshal(body)
		var batch struct {
			Requests []graphBatchRequest `json:"requests"`
		}
		if err := json.Unmarshal(raw, &batch); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(batch.Requests))
		var answer graphBatchResponse
		for _, r := range batch.Requests {
			if r.Method != http.MethodPatch || r.Headers["Prefer"] != `IdType="ImmutableId"` {
				t.Fatalf("update = %#v", r)
			}
			status := http.StatusOK
			for id, code := range answers {
				if r.URL == "/me/messages/"+id {
					status = code
				}
			}
			answer.Responses = append(answer.Responses, struct {
				ID     string          `json:"id"`
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			}{ID: r.ID, Status: status})
		}
		*out.(*graphBatchResponse) = answer
		return nil
	}

	failures := SendOutlookFlagBatch(request, "https://graph.test/v1.0", storage.MessageMutationRead, true, messages)
	if len(sizes) != 2 || sizes[0] != 19 || sizes[1] != 2 {
		t.Fatalf("$batch sizes = %v, want 19 then the pair's 2", sizes)
	}
	for i, err := range failures {
		switch {
		case i == 5 || i == 19:
			if err == nil || errors.Is(err, ErrFlagBatchRefused) {
				t.Fatalf("failures[%d] = %v, want its update's error", i, err)
			}
		case i == 20:
			if !errors.Is(err, ErrFlagBatchRefused) {
				t.Fatalf("oversized message failure = %v, want ErrFlagBatchRefused", err)
			}
		case err != nil:
			// m3 answered 404, which counts as done.
			t.Fatalf("failures[%d] = %v, want nil", i, err)
		}
	}

	down := func(string, string, any, any) error { return errors.New("connection reset") }
	for i, err := range SendOutlookFlagBatch(down, "https://graph.test/v1.0", storage.MessageMutationStarred, true, flagMessages(3)) {
		if err == nil {
			t.Fatalf("failures[%d] = nil after the $batch call failed", i)
		}
	}
}
