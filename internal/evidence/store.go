package evidence

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

var (
	ErrEvidenceNotFound = errors.New("evidence not found")
	ErrEvidenceExists   = errors.New("evidence already exists")
)

// Store manages evidence collected during a diagnosis.
//
// Store is safe for concurrent use.
// Evidence IDs must be unique within a store.
type Store interface {
	Add(item Evidence) error
	Get(id string) (Evidence, error)
	List() []Evidence
	FindBySource(source string) []Evidence
	Len() int
}

// MemoryStore is an in-memory implementation of Store.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]Evidence
}

// NewStore creates an empty in-memory evidence store.
func NewStore() *MemoryStore {
	return &MemoryStore{
		items: make(map[string]Evidence),
	}
}

// Add validates and stores evidence.
//
// Duplicate IDs are rejected instead of silently overwriting
// previously collected observations.
func (s *MemoryStore) Add(item Evidence) error {
	if s == nil {
		return errors.New("evidence store cannot be nil")
	}

	if err := item.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.items == nil {
		s.items = make(map[string]Evidence)
	}

	if _, exists := s.items[item.ID]; exists {
		return ErrEvidenceExists
	}

	s.items[item.ID] = item
	return nil
}

// Get returns evidence by ID.
func (s *MemoryStore) Get(id string) (Evidence, error) {
	if s == nil {
		return Evidence{}, errors.New("evidence store cannot be nil")
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return Evidence{}, ErrEmptyID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	item, exists := s.items[id]
	if !exists {
		return Evidence{}, ErrEvidenceNotFound
	}

	return item, nil
}

// List returns all evidence ordered by creation time, then by ID.
//
// The returned slice is independent of the store's internal map.
func (s *MemoryStore) List() []Evidence {
	if s == nil {
		return []Evidence{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Evidence, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}

		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result
}

// FindBySource returns evidence from the specified source.
//
// Source matching is case-insensitive and ignores surrounding whitespace.
func (s *MemoryStore) FindBySource(source string) []Evidence {
	if s == nil {
		return []Evidence{}
	}

	source = strings.TrimSpace(source)
	if source == "" {
		return []Evidence{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Evidence, 0)
	for _, item := range s.items {
		if strings.EqualFold(strings.TrimSpace(item.Source), source) {
			result = append(result, item)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}

		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result
}

// Len returns the number of stored evidence items.
func (s *MemoryStore) Len() int {
	if s == nil {
		return 0
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.items)
}

// Compile-time interface check.
var _ Store = (*MemoryStore)(nil)
