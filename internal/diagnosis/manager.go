package diagnosis

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/liocoder/TraceMind/internal/evidence"
)

var (
	ErrHypothesisNotFound = errors.New("hypothesis not found")
	ErrHypothesisExists   = errors.New("hypothesis already exists")
	ErrEvidenceStoreNil   = errors.New("evidence store cannot be nil")
)

// Manager manages hypotheses and their evidence relationships.
// Manager is safe for concurrent use.
type Manager struct {
	mu       sync.RWMutex
	items    map[string]Hypothesis
	evidence evidence.Store
}

// NewManager creates a hypothesis manager backed by an evidence store.
func NewManager(store evidence.Store) *Manager {
	return &Manager{
		items:    make(map[string]Hypothesis),
		evidence: store,
	}
}

// validateEvidenceReferences verifies that all linked evidence exists.
func (m *Manager) validateEvidenceReferences(h Hypothesis) error {
	if len(h.Evidence) > 0 && m.evidence == nil {
		return ErrEvidenceStoreNil
	}

	for _, link := range h.Evidence {
		if _, err := m.evidence.Get(link.EvidenceID); err != nil {
			return fmt.Errorf(
				"hypothesis %q references unknown evidence %q: %w",
				h.ID,
				link.EvidenceID,
				err,
			)
		}
	}
	return nil
}

// Add validates and registers a new hypothesis.
func (m *Manager) Add(h Hypothesis) error {
	if m == nil {
		return errors.New("hypothesis manager cannot be nil")
	}
	if err := h.Validate(); err != nil {
		return err
	}

	h.ID = strings.TrimSpace(h.ID)
	h.Title = strings.TrimSpace(h.Title)
	h.Description = strings.TrimSpace(h.Description)
	h.Evidence = cloneLinks(h.Evidence)

	if err := m.validateEvidenceReferences(h); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.items == nil {
		m.items = make(map[string]Hypothesis)
	}
	if _, exists := m.items[h.ID]; exists {
		return fmt.Errorf("%w: %s", ErrHypothesisExists, h.ID)
	}

	m.items[h.ID] = h
	return nil
}

// Update atomically replaces an existing hypothesis.
// All evidence references must already exist in the evidence store.
func (m *Manager) Update(h Hypothesis) error {
	if m == nil {
		return errors.New("hypothesis manager cannot be nil")
	}
	if err := h.Validate(); err != nil {
		return err
	}

	h.ID = strings.TrimSpace(h.ID)
	h.Title = strings.TrimSpace(h.Title)
	h.Description = strings.TrimSpace(h.Description)
	h.Evidence = cloneLinks(h.Evidence)

	if err := m.validateEvidenceReferences(h); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.items[h.ID]; !exists {
		return fmt.Errorf("%w: %s", ErrHypothesisNotFound, h.ID)
	}

	m.items[h.ID] = h
	return nil
}

// Get returns a hypothesis by ID.
func (m *Manager) Get(id string) (Hypothesis, error) {
	if m == nil {
		return Hypothesis{}, errors.New("hypothesis manager cannot be nil")
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return Hypothesis{}, errors.New("hypothesis ID cannot be empty")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	h, exists := m.items[id]
	if !exists {
		return Hypothesis{}, fmt.Errorf("%w: %s", ErrHypothesisNotFound, id)
	}

	return cloneHypothesis(h), nil
}

// List returns all hypotheses in deterministic ID order.
func (m *Manager) List() []Hypothesis {
	if m == nil {
		return []Hypothesis{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Hypothesis, 0, len(m.items))
	for _, h := range m.items {
		result = append(result, cloneHypothesis(h))
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})

	return result
}

// AddEvidence links a new evidence item to an existing hypothesis.
func (m *Manager) AddEvidence(
	hypothesisID string,
	link EvidenceLink,
) error {
	if m == nil {
		return errors.New("hypothesis manager cannot be nil")
	}
	if m.evidence == nil {
		return ErrEvidenceStoreNil
	}

	hypothesisID = strings.TrimSpace(hypothesisID)
	if hypothesisID == "" {
		return errors.New("hypothesis ID cannot be empty")
	}

	link.EvidenceID = strings.TrimSpace(link.EvidenceID)
	link.Reason = strings.TrimSpace(link.Reason)

	if link.EvidenceID == "" {
		return errors.New("evidence link ID cannot be empty")
	}
	if link.Reason == "" {
		return errors.New("evidence link reason cannot be empty")
	}

	switch link.Relation {
	case RelationSupports, RelationRefutes, RelationNeutral:
	default:
		return fmt.Errorf("invalid evidence relation: %q", link.Relation)
	}

	if _, err := m.evidence.Get(link.EvidenceID); err != nil {
		return fmt.Errorf("evidence %q does not exist: %w", link.EvidenceID, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	h, exists := m.items[hypothesisID]
	if !exists {
		return fmt.Errorf("%w: %s", ErrHypothesisNotFound, hypothesisID)
	}

	if err := (&h).AddEvidence(link); err != nil {
		return err
	}

	m.items[hypothesisID] = h
	return nil
}

// UpdateAssessment updates the status and confidence of a hypothesis.
func (m *Manager) UpdateAssessment(
	id string,
	status Status,
	confidence float64,
) error {
	if m == nil {
		return errors.New("hypothesis manager cannot be nil")
	}

	switch status {
	case StatusProposed, StatusSupported, StatusRefuted, StatusInconclusive:
	default:
		return fmt.Errorf("invalid hypothesis status: %q", status)
	}

	if math.IsNaN(confidence) ||
		math.IsInf(confidence, 0) ||
		confidence < 0 ||
		confidence > 1 {
		return errors.New("hypothesis confidence must be finite and between 0 and 1")
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("hypothesis ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	h, exists := m.items[id]
	if !exists {
		return fmt.Errorf("%w: %s", ErrHypothesisNotFound, id)
	}

	h.Status = status
	h.Confidence = confidence
	m.items[id] = h

	return nil
}

// Len returns the number of registered hypotheses.
func (m *Manager) Len() int {
	if m == nil {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.items)
}

func cloneHypothesis(h Hypothesis) Hypothesis {
	h.Evidence = cloneLinks(h.Evidence)
	return h
}

func cloneLinks(links []EvidenceLink) []EvidenceLink {
	if len(links) == 0 {
		return nil
	}

	result := make([]EvidenceLink, len(links))
	copy(result, links)
	return result
}
