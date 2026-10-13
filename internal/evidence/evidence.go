package evidence

import "time"

// Evidence represents an observation collected during diagnosis.
type Evidence struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// New creates a new piece of evidence.
func New(id, source, content string) Evidence {
	return Evidence{
		ID:        id,
		Source:    source,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
}
