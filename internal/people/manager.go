// Package people manages person/contact records with fuzzy name matching
// and optional semantic search via embeddings.
package people

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/you/claude-gateway/internal/ollama"
	"github.com/you/claude-gateway/internal/pgdb"
)

// Manager provides CRUD and lookup operations for people and contacts.
type Manager struct {
	db       *pgdb.DB
	embedder *ollama.Client
}

// NewManager creates a new people manager. Panics if db is nil because
// every Manager method depends on a valid database connection.
func NewManager(db *pgdb.DB, embedder *ollama.Client) *Manager {
	if db == nil {
		panic("people: nil db")
	}
	return &Manager{db: db, embedder: embedder}
}

// AddPerson creates a new person record with optional embedding.
func (m *Manager) AddPerson(ctx context.Context, fullName, preferredName, pType, role, company, notes string) (*pgdb.Person, error) {
	if fullName == "" {
		return nil, fmt.Errorf("full name is required")
	}
	if pType == "" {
		pType = "contact"
	}

	p := &pgdb.Person{
		FullName:      fullName,
		PreferredName: preferredName,
		Type:          pType,
		Role:          role,
		Company:       company,
		Notes:         notes,
	}

	// Generate embedding from concatenated fields
	var embedding []float32
	if m.embedder != nil {
		text := buildPersonText(fullName, preferredName, pType, role, company, notes)
		if emb, err := m.embedder.Embed(ctx, text); err == nil {
			embedding = emb
		} else {
			log.Printf("People: embedding failed for %q: %v", fullName, err)
		}
	}

	id, err := m.db.SavePerson(ctx, p, embedding)
	if err != nil {
		return nil, fmt.Errorf("adding person: %w", err)
	}
	p.ID = id
	return p, nil
}

// UpdatePerson updates fields on an existing person. Only non-empty values are updated.
func (m *Manager) UpdatePerson(ctx context.Context, id int, updates map[string]string) error {
	existing, err := m.db.GetPersonByID(ctx, id)
	if err != nil {
		return fmt.Errorf("loading person for update: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("person %d not found", id)
	}

	p := &existing.Person
	if v, ok := updates["full_name"]; ok && v != "" {
		p.FullName = v
	}
	if v, ok := updates["preferred_name"]; ok {
		p.PreferredName = v
	}
	if v, ok := updates["type"]; ok && v != "" {
		p.Type = v
	}
	if v, ok := updates["role"]; ok {
		p.Role = v
	}
	if v, ok := updates["company"]; ok {
		p.Company = v
	}
	if v, ok := updates["notes"]; ok {
		p.Notes = v
	}

	// Regenerate embedding
	var embedding []float32
	if m.embedder != nil {
		text := buildPersonText(p.FullName, p.PreferredName, p.Type, p.Role, p.Company, p.Notes)
		if emb, err := m.embedder.Embed(ctx, text); err == nil {
			embedding = emb
		} else {
			log.Printf("People: embedding failed for %q: %v", p.FullName, err)
		}
	}

	return m.db.UpdatePerson(ctx, id, p, embedding)
}

// RemovePerson soft-deletes a person.
func (m *Manager) RemovePerson(ctx context.Context, id int) error {
	return m.db.DeletePerson(ctx, id)
}

// FindPerson finds a person by name, using trigram matching first and semantic fallback.
func (m *Manager) FindPerson(ctx context.Context, query string) (*pgdb.PersonWithContacts, error) {
	// Try trigram name match first
	p, err := m.db.FindPersonByName(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("find person: %w", err)
	}
	if p != nil {
		return p, nil
	}

	// Semantic fallback if embedder is available
	if m.embedder != nil {
		emb, err := m.embedder.Embed(ctx, query)
		if err == nil {
			p, err = m.db.FindPersonByEmbedding(ctx, emb, 0.75)
			if err != nil {
				return nil, fmt.Errorf("find person by embedding: %w", err)
			}
			return p, nil
		}
	}

	return nil, nil
}

// FindPeople looks up multiple people by name, deduplicating results.
// Returns partial results alongside any accumulated errors from individual lookups.
func (m *Manager) FindPeople(ctx context.Context, names []string) ([]pgdb.PersonWithContacts, error) {
	seen := make(map[int]bool)
	var results []pgdb.PersonWithContacts
	var errs []error

	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		p, err := m.FindPerson(ctx, name)
		if err != nil {
			log.Printf("FindPeople: lookup failed for %q: %v", name, err)
			errs = append(errs, fmt.Errorf("lookup %q: %w", name, err))
			continue
		}
		if p != nil && !seen[p.ID] {
			seen[p.ID] = true
			results = append(results, *p)
		}
	}
	return results, errors.Join(errs...)
}

// ListPeople returns all active people.
func (m *Manager) ListPeople(ctx context.Context) ([]pgdb.PersonWithContacts, error) {
	return m.db.ListPeople(ctx)
}

// AddContact adds a contact detail to a person.
func (m *Manager) AddContact(ctx context.Context, personID int, contactType, value, label string) error {
	return m.db.SaveContactDetail(ctx, personID, contactType, value, label)
}

// buildPersonText concatenates person fields for embedding generation.
func buildPersonText(fullName, preferredName, pType, role, company, notes string) string {
	parts := []string{fullName}
	if preferredName != "" {
		parts = append(parts, "also known as "+preferredName)
	}
	if pType != "" && pType != "contact" {
		parts = append(parts, pType)
	}
	if role != "" {
		parts = append(parts, role)
	}
	if company != "" {
		parts = append(parts, "at "+company)
	}
	if notes != "" {
		parts = append(parts, notes)
	}
	return strings.Join(parts, ". ")
}

// MatchNames scans the message text for known people names (full name and
// preferred name) using case-insensitive substring matching. Returns the
// matched names suitable for passing to FindPeople / BuildEnrichedPrompt.
// This is a fast, in-process replacement for the Ollama triage NER step.
func (m *Manager) MatchNames(ctx context.Context, text string) ([]string, error) {
	people, err := m.db.ListPeople(ctx)
	if err != nil {
		return nil, err
	}

	lower := strings.ToLower(text)
	seen := make(map[string]bool)
	var matched []string

	for _, p := range people {
		// Check preferred name first (shorter, more likely to appear)
		name := p.PreferredName
		if name == "" {
			name = p.FullName
		}
		if name == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(name)) && !seen[name] {
			seen[name] = true
			matched = append(matched, name)
		}
		// Also check full name if different from preferred
		if p.FullName != "" && p.FullName != name {
			if strings.Contains(lower, strings.ToLower(p.FullName)) && !seen[p.FullName] {
				seen[p.FullName] = true
				matched = append(matched, p.FullName)
			}
		}
	}

	return matched, nil
}
