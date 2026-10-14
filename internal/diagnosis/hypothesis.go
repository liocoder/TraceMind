package diagnosis

import (
	"errors"
	"fmt"
	"strings"
)

// Status describes the current assessment of a hypothesis.
type Status string

const (
	StatusProposed     Status = "proposed"
	StatusSupported    Status = "supported"
	StatusRefuted      Status = "refuted"
	StatusInconclusive Status = "inconclusive"
)

// EvidenceRelation describes how an observation relates to a hypothesis.
type EvidenceRelation string

const (
	RelationSupports EvidenceRelation = "supports"
	RelationRefutes  EvidenceRelation = "refutes"
	RelationNeutral  EvidenceRelation = "neutral"
)

// EvidenceLink connects a hypothesis to a collected observation.
//
// EvidenceID references an item in the evidence store.
// Reason explains why the observation matters to this hypothesis.
type EvidenceLink struct {
	EvidenceID string           `json:"evidence_id"`
	Relation   EvidenceRelation `json:"relation"`
	Reason     string           `json:"reason"`
}

// Hypothesis represents a possible root cause of an incident.
type Hypothesis struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Status      Status         `json:"status"`
	Confidence  float64        `json:"confidence"`
	Evidence    []EvidenceLink `json:"evidence"`
}

// Validate checks the structural integrity of a hypothesis.
func (h Hypothesis) Validate() error {
	if strings.TrimSpace(h.ID) == "" {
		return errors.New("hypothesis ID cannot be empty")
	}
	if strings.TrimSpace(h.Title) == "" {
		return errors.New("hypothesis title cannot be empty")
	}
	if strings.TrimSpace(h.Description) == "" {
		return errors.New("hypothesis description cannot be empty")
	}

	switch h.Status {
	case StatusProposed, StatusSupported, StatusRefuted, StatusInconclusive:
	default:
		return fmt.Errorf("invalid hypothesis status: %q", h.Status)
	}

	if h.Confidence < 0 || h.Confidence > 1 {
		return errors.New("hypothesis confidence must be between 0 and 1")
	}

	seen := make(map[string]struct{}, len(h.Evidence))
	for _, link := range h.Evidence {
		if strings.TrimSpace(link.EvidenceID) == "" {
			return errors.New("evidence link ID cannot be empty")
		}
		if strings.TrimSpace(link.Reason) == "" {
			return errors.New("evidence link reason cannot be empty")
		}

		switch link.Relation {
		case RelationSupports, RelationRefutes, RelationNeutral:
		default:
			return fmt.Errorf("invalid evidence relation: %q", link.Relation)
		}

		if _, exists := seen[link.EvidenceID]; exists {
			return fmt.Errorf("duplicate evidence link: %s", link.EvidenceID)
		}
		seen[link.EvidenceID] = struct{}{}
	}

	return nil
}

// AddEvidence attaches an observation to the hypothesis.
// The same evidence ID cannot be attached twice.
func (h *Hypothesis) AddEvidence(link EvidenceLink) error {
	if h == nil {
		return errors.New("hypothesis cannot be nil")
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

	for _, existing := range h.Evidence {
		if existing.EvidenceID == link.EvidenceID {
			return fmt.Errorf("evidence %q is already linked", link.EvidenceID)
		}
	}

	h.Evidence = append(h.Evidence, link)
	return nil
}
