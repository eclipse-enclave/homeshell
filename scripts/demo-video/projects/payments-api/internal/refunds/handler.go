// Package refunds takes refund requests and tells the merchant once one is booked.
package refunds

import (
	"encoding/json"
	"net/http"
	"sync"
)

type Refund struct {
	ID        string `json:"id"`
	PaymentID string `json:"paymentId"`
	Cents     int64  `json:"cents"`
}

type Notifier interface {
	Notify(event string, payload any) error
}

type Store interface {
	Save(r Refund) (Refund, error)
}

type MemoryStore struct {
	mu      sync.Mutex
	refunds []Refund
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (s *MemoryStore) Save(r Refund) (Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = "re_" + string(rune('a'+len(s.refunds)))
	s.refunds = append(s.refunds, r)
	return r, nil
}

type Handler struct {
	store Store
	hooks Notifier
}

func NewHandler(store Store, hooks Notifier) *Handler { return &Handler{store, hooks} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in Refund
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Cents <= 0 {
		http.Error(w, "invalid refund", http.StatusBadRequest)
		return
	}
	saved, err := h.store.Save(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.hooks.Notify("refund.created", saved)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(saved)
}
