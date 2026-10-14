package evidence

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrEmptyID      = errors.New("evidence ID cannot be empty")
	ErrEmptySource  = errors.New("evidence source cannot be empty")
	ErrEmptyContent = errors.New("evidence content cannot be empty")
)

// Evidence represents an observation collected during diagnosis.
type Evidence struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// New creates a new piece of evidence.
//
// ID, source and content are trimmed to avoid accidental whitespace.
// CreatedAt is recorded in UTC.
func New(id, source, content string) Evidence {
	return Evidence{
		ID:        strings.TrimSpace(id),
		Source:    strings.TrimSpace(source),
		Content:   strings.TrimSpace(content),
		CreatedAt: time.Now().UTC(),
	}
}

// Validate checks whether the evidence contains the required fields.
func (e Evidence) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return ErrEmptyID
	}

	if strings.TrimSpace(e.Source) == "" {
		return ErrEmptySource
	}

	if strings.TrimSpace(e.Content) == "" {
		return ErrEmptyContent
	}

	if e.CreatedAt.IsZero() {
		return errors.New("evidence creation time cannot be zero")
	}

	return nil
}
