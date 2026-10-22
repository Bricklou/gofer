package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// contactSelectionLimit caps how many contacts "select all" and an export of
// the selection take at once.
const contactSelectionLimit = 10000

type contactSelectionItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// handleContactIDs lists every contact the list shows under the given
// filters, in list order, with its name and address, so the list can select
// all of them and not just the loaded ones, and write to them.
func (h *Handler) handleContactIDs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := h.userID(ctx)
	result := struct {
		Contacts  []contactSelectionItem `json:"contacts"`
		Truncated bool                   `json:"truncated"`
	}{Contacts: []contactSelectionItem{}}
	err := h.withUserDB(ctx, userID, func(db *storage.DB) error {
		filters := applyContactSortDefaults(h.parseContactFilters(r), r, db.GetUISettings(ctx, userID))
		contacts, truncated, err := db.ListContactsForSelection(ctx, userID, filters, contactSelectionLimit)
		if err != nil {
			return err
		}
		result.Truncated = truncated
		for _, contact := range contacts {
			result.Contacts = append(result.Contacts, contactSelectionItem{ID: contact.ID, Name: contact.Name, Email: strings.TrimSpace(contact.Email)})
		}
		return nil
	})
	if err != nil {
		log.Printf("contact ids unavailable: %v", err)
		http.Error(w, "contacts unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	json.NewEncoder(w).Encode(result)
}

// handleExportSelectedContacts gives the selected contacts, named by id in
// the posted form, as one vCard file. Ids that aren't the user's contacts are
// left out.
func (h *Handler) handleExportSelectedContacts(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var ids []string
	seen := make(map[string]bool)
	for _, id := range r.PostForm["id"] {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > contactSelectionLimit {
		http.Error(w, "choose between 1 and 10000 contacts", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	userID := h.userID(ctx)
	var contacts []models.Contact
	err := h.withUserDB(ctx, userID, func(db *storage.DB) error {
		for _, id := range ids {
			contact, err := db.GetContact(ctx, userID, id)
			if err != nil {
				return err
			}
			if contact != nil {
				contacts = append(contacts, *contact)
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, "failed to export contacts", http.StatusInternalServerError)
		return
	}
	if len(contacts) == 0 {
		http.NotFound(w, r)
		return
	}
	filename := "gofer-contacts.vcf"
	if len(contacts) == 1 {
		filename = contactVCardFilename(contacts[0])
	}
	serveVCard(w, filename, contacts)
}
