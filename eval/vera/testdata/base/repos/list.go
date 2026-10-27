package repos

import (
	"encoding/json"
	"net/http"
)

// ListLinkedRepos serves GET /repos: every linked repository with its
// settings, for the project's settings page.
func (h *Handlers) ListLinkedRepos(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{"repositories": h.Store.List()})
}
