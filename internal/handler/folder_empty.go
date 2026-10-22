package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// handleEmptyFolder empties Trash for good, or moves everything in Spam to
// Trash, in one request however large the folder is. Each message is still
// queued for its provider, as deleting or moving it alone would be.
func (h *Handler) handleEmptyFolder(w http.ResponseWriter, r *http.Request) {
	ctx := h.messageMutationContext(r.Context())
	userID := h.userID(ctx)
	folderID, err := h.resolveFolderID(ctx, userID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, storage.ErrAccountRoute) {
			http.NotFound(w, r)
			return
		}
		log.Printf("empty folder: resolve folder: %v", err)
		http.Error(w, "mailbox unavailable", http.StatusServiceUnavailable)
		return
	}
	// With per-user storage, an account whose changes can't be delivered
	// yet is left as it is; elsewhere every account is allowed.
	var refused error
	result, err := h.db.EmptyFolderForUser(ctx, folderID, userID, func(accountID string) bool {
		err := h.checkUserMessageMutation(ctx, &storage.MessageMutationInfo{AccountID: accountID})
		if err != nil && refused == nil {
			refused = err
		}
		return err == nil
	})
	switch {
	case errors.Is(err, storage.ErrFolderNotEmptiable):
		http.Error(w, "Only Trash and Spam can be emptied.", http.StatusBadRequest)
		return
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
		return
	case err != nil:
		log.Printf("empty folder %s: %v", folderID, err)
		http.Error(w, "failed to empty folder", http.StatusInternalServerError)
		return
	}
	if result.Removed == 0 && refused != nil {
		writeMessageTargetError(w, r, refused)
		return
	}
	if result.Removed > 0 {
		h.signalMessageMutationWorker()
	}
	// One notice per folder rather than one per message.
	for _, folder := range result.Folders {
		h.publishMutation(folder.AccountID, folder.FolderID)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"removed": result.Removed, "skipped_accounts": refused != nil, "no_trash": result.NoTrash})
}
