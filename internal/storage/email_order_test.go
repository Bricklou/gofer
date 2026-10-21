package storage

import (
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

func TestEmailFlagOrderSQL(t *testing.T) {
	cases := []struct {
		sortBy, order, want string
	}{
		{"unread", "desc", "COALESCE(thread_is_read, 0) ASC"},
		{"unread", "asc", "COALESCE(thread_is_read, 0) DESC"},
		{"starred", "desc", "COALESCE(thread_is_starred, 0) DESC"},
		{"starred", "asc", "COALESCE(thread_is_starred, 0) ASC"},
		{"attachments", "desc", "COALESCE(thread_has_attachments, 0) DESC"},
	}
	for _, tc := range cases {
		filters := models.EmailFilters{SortBy: tc.sortBy, SortOrder: tc.order}
		if got := emailFlagOrderSQL(filters, "thread_is_read", "thread_is_starred", "thread_has_attachments"); got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.sortBy, tc.order, got, tc.want)
		}
	}
}
