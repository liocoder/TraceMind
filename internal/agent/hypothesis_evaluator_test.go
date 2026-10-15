package agent

import (
	"testing"

	"github.com/liocoder/TraceMind/internal/diagnosis"
)

func TestConstrainHypothesisAssessment(t *testing.T) {
	tests := []struct {
		name           string
		status         diagnosis.Status
		confidence     float64
		relations      []diagnosis.EvidenceRelation
		wantStatus     diagnosis.Status
		wantConfidence float64
	}{
		{
			name:       "conflicting evidence becomes inconclusive",
			status:     diagnosis.StatusSupported,
			confidence: 0.9,
			relations: []diagnosis.EvidenceRelation{
				diagnosis.RelationSupports,
				diagnosis.RelationRefutes,
			},
			wantStatus:     diagnosis.StatusInconclusive,
			wantConfidence: 0.5,
		},
		{
			name:       "supported without supporting evidence becomes proposed",
			status:     diagnosis.StatusSupported,
			confidence: 0.8,
			relations: []diagnosis.EvidenceRelation{
				diagnosis.RelationNeutral,
			},
			wantStatus:     diagnosis.StatusProposed,
			wantConfidence: 0.4,
		},
		{
			name:       "refuted without refuting evidence becomes proposed",
			status:     diagnosis.StatusRefuted,
			confidence: 0.8,
			relations: []diagnosis.EvidenceRelation{
				diagnosis.RelationNeutral,
			},
			wantStatus:     diagnosis.StatusProposed,
			wantConfidence: 0.4,
		},
		{
			name:       "supported with supporting evidence remains supported",
			status:     diagnosis.StatusSupported,
			confidence: 0.8,
			relations: []diagnosis.EvidenceRelation{
				diagnosis.RelationSupports,
			},
			wantStatus:     diagnosis.StatusSupported,
			wantConfidence: 0.8,
		},
		{
			name:       "refuted with refuting evidence remains refuted",
			status:     diagnosis.StatusRefuted,
			confidence: 0.7,
			relations: []diagnosis.EvidenceRelation{
				diagnosis.RelationRefutes,
			},
			wantStatus:     diagnosis.StatusRefuted,
			wantConfidence: 0.7,
		},
		{
			name:       "conflicting evidence does not increase confidence",
			status:     diagnosis.StatusSupported,
			confidence: 0.3,
			relations: []diagnosis.EvidenceRelation{
				diagnosis.RelationSupports,
				diagnosis.RelationRefutes,
			},
			wantStatus:     diagnosis.StatusInconclusive,
			wantConfidence: 0.3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links := make([]diagnosis.EvidenceLink, 0, len(tt.relations))

			for i, relation := range tt.relations {
				links = append(links, diagnosis.EvidenceLink{
					EvidenceID: "ev-test-" + string(rune('1'+i)),
					Relation:   relation,
					Reason:     "test evidence relationship",
				})
			}

			gotStatus, gotConfidence := constrainHypothesisAssessment(
				tt.status,
				tt.confidence,
				links,
			)

			if gotStatus != tt.wantStatus {
				t.Errorf(
					"status = %q, want %q",
					gotStatus,
					tt.wantStatus,
				)
			}

			if gotConfidence != tt.wantConfidence {
				t.Errorf(
					"confidence = %v, want %v",
					gotConfidence,
					tt.wantConfidence,
				)
			}
		})
	}
}

func TestConstrainHypothesisAssessmentAdditionalCases(t *testing.T) {
	tests := []struct {
		name           string
		status         diagnosis.Status
		confidence     float64
		relations      []diagnosis.EvidenceRelation
		wantStatus     diagnosis.Status
		wantConfidence float64
	}{
		{
			name:           "neutral evidence cannot establish support",
			status:         diagnosis.StatusSupported,
			confidence:     0.95,
			relations:      []diagnosis.EvidenceRelation{diagnosis.RelationNeutral},
			wantStatus:     diagnosis.StatusProposed,
			wantConfidence: 0.4,
		},
		{
			name:           "refuting evidence overrides unsupported refuted claim",
			status:         diagnosis.StatusRefuted,
			confidence:     0.9,
			relations:      []diagnosis.EvidenceRelation{diagnosis.RelationSupports},
			wantStatus:     diagnosis.StatusInconclusive,
			wantConfidence: 0.4,
		},
		{
			name:           "conflicting evidence caps confidence",
			status:         diagnosis.StatusSupported,
			confidence:     0.95,
			relations:      []diagnosis.EvidenceRelation{diagnosis.RelationSupports, diagnosis.RelationRefutes},
			wantStatus:     diagnosis.StatusInconclusive,
			wantConfidence: 0.5,
		},
		{
			name:           "already low confidence stays low with conflicting evidence",
			status:         diagnosis.StatusSupported,
			confidence:     0.2,
			relations:      []diagnosis.EvidenceRelation{diagnosis.RelationSupports, diagnosis.RelationRefutes},
			wantStatus:     diagnosis.StatusInconclusive,
			wantConfidence: 0.2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links := make([]diagnosis.EvidenceLink, 0, len(tt.relations))
			for i, relation := range tt.relations {
				links = append(links, diagnosis.EvidenceLink{
					EvidenceID: "ev-extra-" + string(rune('1'+i)),
					Relation:   relation,
					Reason:     "test evidence relationship",
				})
			}

			gotStatus, gotConfidence := constrainHypothesisAssessment(
				tt.status,
				tt.confidence,
				links,
			)

			if gotStatus != tt.wantStatus {
				t.Errorf("status = %q, want %q", gotStatus, tt.wantStatus)
			}

			if gotConfidence != tt.wantConfidence {
				t.Errorf("confidence = %v, want %v", gotConfidence, tt.wantConfidence)
			}
		})
	}
}
