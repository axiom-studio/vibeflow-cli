package repos

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Handlers serves the linked-repository API to project members.
type Handlers struct {
	Store *Store
}

func repoID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// redacted returns repo without its webhook secret, for API responses.
func redacted(repo LinkedRepo) LinkedRepo {
	repo.Settings.WebhookSecret = ""
	return repo
}

// GetLinkedRepo serves GET /repos/{id}.
func (h *Handlers) GetLinkedRepo(w http.ResponseWriter, r *http.Request) {
	id, err := repoID(r)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	repo, err := h.Store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(redacted(repo))
}

// UpdateLinkedRepo serves PUT /repos/{id} with the repository's new settings.
// The webhook secret is not editable here; it keeps its stored value.
func (h *Handlers) UpdateLinkedRepo(w http.ResponseWriter, r *http.Request) {
	id, err := repoID(r)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	repo, err := h.Store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var settings Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "bad settings", http.StatusBadRequest)
		return
	}
	settings.WebhookSecret = repo.Settings.WebhookSecret
	repo.Settings = settings
	h.Store.Put(repo)
	json.NewEncoder(w).Encode(redacted(repo))
}
