package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/liocoder/TraceMind/internal/diagnosis"
	"github.com/liocoder/TraceMind/internal/evidence"
	"github.com/liocoder/TraceMind/internal/llm"
)

type hypothesisEvaluation struct {
	ID          string                   `json:"id"`
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Status      diagnosis.Status         `json:"status"`
	Confidence  float64                  `json:"confidence"`
	Evidence    []diagnosis.EvidenceLink `json:"evidence"`
}

type hypothesisEvaluationResponse struct {
	Hypotheses []hypothesisEvaluation `json:"hypotheses"`
}

func evaluateHypotheses(
	ctx context.Context,
	client completionClient,
	incident string,
	store evidence.Store,
	manager *diagnosis.Manager,
) error {
	if ctx == nil {
		return errors.New("evaluation context cannot be nil")
	}
	if client == nil {
		return errors.New("evaluation LLM client cannot be nil")
	}
	if store == nil {
		return errors.New("evaluation evidence store cannot be nil")
	}
	if manager == nil {
		return errors.New("evaluation diagnosis manager cannot be nil")
	}

	items := store.List()
	if len(items) == 0 {
		return nil
	}

	existing := manager.List()

	payload, err := json.Marshal(struct {
		Incident   string                 `json:"incident"`
		Evidence   []evidence.Evidence    `json:"evidence"`
		Hypotheses []diagnosis.Hypothesis `json:"existing_hypotheses"`
	}{
		Incident:   incident,
		Evidence:   items,
		Hypotheses: existing,
	})
	if err != nil {
		return fmt.Errorf("marshal evaluation context: %w", err)
	}

	prompt := `You are an evidence-based incident hypothesis evaluator.

Analyze the incident, collected evidence, and existing hypotheses.
Return valid JSON only:
{
  "hypotheses": [
    {
      "id": "hyp-xxxxxxxx",
      "title": "Short hypothesis title",
      "description": "What may have caused the incident",
      "status": "proposed",
      "confidence": 0.5,
      "evidence": [
        {
          "evidence_id": "ev-001",
          "relation": "supports",
          "reason": "Why this evidence supports the hypothesis"
        }
      ]
    }
  ]
}

Rules:
1. Every evidence_id must match an ID in the supplied evidence.
2. Every hypothesis must cite at least one relevant evidence item.
3. relation must be supports, refutes, or neutral.
4. status must be proposed, supported, refuted, or inconclusive.
5. confidence must be between 0 and 1. It is a judgment, not a statistical probability.
6. Reuse the ID of an existing hypothesis when updating it.
7. For a new hypothesis, use hyp- followed by 8 lowercase hexadecimal characters.
8. Never invent observations. Correlation alone does not prove causality.
9. Preserve plausible existing hypotheses when evidence is inconclusive.
10. Return an empty hypotheses array if no defensible hypothesis can be formed.
11. Do not use Markdown code fences.

Input data:
` + string(payload)

	completion, err := client.Complete(
		ctx,
		[]llm.Message{{Role: "system", Content: prompt}},
		nil,
	)
	if err != nil {
		return fmt.Errorf("call hypothesis evaluator: %w", err)
	}
	if completion == nil {
		return errors.New("hypothesis evaluator returned nil completion")
	}

	content := strings.TrimSpace(completion.Message.Content)
	if content == "" {
		return errors.New("hypothesis evaluator returned empty content")
	}

	var response hypothesisEvaluationResponse
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return fmt.Errorf("parse hypothesis evaluation JSON: %w", err)
	}

	existingByTitle := make(map[string]diagnosis.Hypothesis)
	for _, h := range existing {
		existingByTitle[normalizeHypothesisTitle(h.Title)] = h
	}

	for _, item := range response.Hypotheses {
		item.Title = strings.TrimSpace(item.Title)
		item.Description = strings.TrimSpace(item.Description)
		item.ID = strings.TrimSpace(item.ID)

		if item.Title == "" || item.Description == "" {
			continue
		}

		if math.IsNaN(item.Confidence) ||
			math.IsInf(item.Confidence, 0) ||
			item.Confidence < 0 ||
			item.Confidence > 1 {
			continue
		}

		// Reuse an existing ID if the model returned a different ID
		// for a hypothesis with the same normalized title.
		if old, ok := existingByTitle[normalizeHypothesisTitle(item.Title)]; ok {
			item.ID = old.ID
		}
		if item.ID == "" {
			item.ID = stableHypothesisID(item.Title)
		}

		validLinks := make([]diagnosis.EvidenceLink, 0, len(item.Evidence))
		seenLinks := make(map[string]struct{})

		for _, link := range item.Evidence {
			link.EvidenceID = strings.TrimSpace(link.EvidenceID)
			link.Reason = strings.TrimSpace(link.Reason)

			if link.EvidenceID == "" || link.Reason == "" {
				continue
			}
			if _, duplicate := seenLinks[link.EvidenceID]; duplicate {
				continue
			}
			if _, err := store.Get(link.EvidenceID); err != nil {
				continue
			}

			switch link.Relation {
			case diagnosis.RelationSupports,
				diagnosis.RelationRefutes,
				diagnosis.RelationNeutral:
			default:
				continue
			}

			seenLinks[link.EvidenceID] = struct{}{}
			validLinks = append(validLinks, link)
		}

		if len(validLinks) == 0 {
			continue
		}

		candidate := diagnosis.Hypothesis{
			ID:          item.ID,
			Title:       item.Title,
			Description: item.Description,
			Status:      item.Status,
			Confidence:  item.Confidence,
			Evidence:    validLinks,
		}

		// Validate the complete hypothesis before mutating the manager.
		if err := candidate.Validate(); err != nil {
			continue
		}

		current, err := manager.Get(item.ID)
		if err == nil {
			if err := manager.UpdateAssessment(
				item.ID,
				item.Status,
				item.Confidence,
			); err != nil {
				return fmt.Errorf("update hypothesis %q: %w", item.ID, err)
			}

			for _, link := range validLinks {
				if hasEvidenceLink(current, link.EvidenceID) {
					continue
				}
				if err := manager.AddEvidence(item.ID, link); err != nil {
					return fmt.Errorf(
						"link evidence %q to hypothesis %q: %w",
						link.EvidenceID,
						item.ID,
						err,
					)
				}
			}
			continue
		}

		if !errors.Is(err, diagnosis.ErrHypothesisNotFound) {
			return fmt.Errorf("get hypothesis %q: %w", item.ID, err)
		}

		if err := manager.Add(candidate); err != nil {
			return fmt.Errorf("add hypothesis %q: %w", item.ID, err)
		}
	}

	return nil
}

func hasEvidenceLink(
	h diagnosis.Hypothesis,
	evidenceID string,
) bool {
	for _, link := range h.Evidence {
		if link.EvidenceID == evidenceID {
			return true
		}
	}
	return false
}

func normalizeHypothesisTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

func stableHypothesisID(title string) string {
	sum := sha256.Sum256([]byte(normalizeHypothesisTitle(title)))
	return "hyp-" + hex.EncodeToString(sum[:4])
}
