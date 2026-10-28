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
	json.NewEncoder(w).Encode(repo)
}

// UpdateLinkedRepo serves PUT /repos/{id} with the repository's new settings.
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
	repo.Settings = settings
	h.Store.Put(repo)
	json.NewEncoder(w).Encode(repo)
}
