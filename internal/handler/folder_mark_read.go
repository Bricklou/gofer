package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// handleMarkFolderRead marks every unread message in a folder as read in one
// request, however large the folder is. Each message is still queued for its
// provider, so sync keeps the change until the provider has it.
func (h *Handler) handleMarkFolderRead(w http.ResponseWriter, r *http.Request) {
	ctx := h.messageMutationContext(r.Context())
	userID := h.userID(ctx)
	folderID, err := h.resolveFolderID(ctx, userID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, storage.ErrAccountRoute) {
			http.NotFound(w, r)
			return
		}
		log.Printf("mark folder read: resolve folder: %v", err)
		http.Error(w, "mailbox unavailable", http.StatusServiceUnavailable)
		return
	}
	// With per-user storage, an account whose changes can't be delivered
	// yet is left as it is; elsewhere every account is allowed.
	var refused error
	result, err := h.db.MarkFolderReadForUser(ctx, folderID, userID, func(accountID string) bool {
		err := h.checkUserMessageMutation(ctx, &storage.MessageMutationInfo{AccountID: accountID})
		if err != nil && refused == nil {
			refused = err
		}
		return err == nil
	})
	if errors.Is(err, storage.ErrFolderNotMarkable) {
		http.Error(w, "This folder can't be marked as read.", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("mark folder read %s: %v", folderID, err)
		http.Error(w, "failed to mark folder as read", http.StatusInternalServerError)
		return
	}
	if result.Marked == 0 && refused != nil {
		writeMessageTargetError(w, r, refused)
		return
	}
	if result.Marked > 0 {
		h.signalMessageMutationWorker()
	}
	// One notice per folder rather than one per message.
	for _, folder := range result.Folders {
		h.publishMutation(folder.AccountID, folder.FolderID)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"marked": result.Marked, "skipped_accounts": refused != nil})
}
