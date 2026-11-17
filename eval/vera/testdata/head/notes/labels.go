// Package notes keeps labels on project notes in PostgreSQL. The labels
// table is the source of truth; notes.labels_json is the copy the note list
// shows.
package notes

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNotFound is returned for a note that does not exist in the project.
var ErrNotFound = errors.New("notes: note not found")

// Schema is the part of the database this package uses.
const Schema = `
CREATE TABLE notes (
	id          text PRIMARY KEY,
	project_id  text NOT NULL,
	body        text NOT NULL,
	labels_json jsonb
);
CREATE TABLE labels (
	note_id text NOT NULL REFERENCES notes(id),
	name    text NOT NULL,
	color   text NOT NULL,
	PRIMARY KEY (note_id, name)
);`

// lockNoteSQL takes the note's row lock. Every label writer takes it before
// touching labels, so writers on one note run one after another and each
// rewrite of labels_json reads every committed label.
const lockNoteSQL = `SELECT id FROM notes WHERE id = $1 AND project_id = $2 FOR UPDATE`

const insertLabelSQL = `
INSERT INTO labels(note_id, name, color) VALUES ($1, $2, $3)
ON CONFLICT (note_id, name) DO UPDATE SET color = excluded.color`

const deleteLabelSQL = `DELETE FROM labels WHERE note_id = $1 AND name = $2`

const syncLabelsSQL = `
UPDATE notes SET labels_json = (SELECT jsonb_object_agg(name, color) FROM labels WHERE note_id = $1)
WHERE id = $1`

// Store reads and writes labels.
type Store struct {
	DB *sql.DB
}

// SetLabel sets one label on a note.
func (s *Store) SetLabel(ctx context.Context, projectID, noteID, name, color string) error {
	return s.write(ctx, projectID, noteID, insertLabelSQL, noteID, name, color)
}

// RemoveLabel removes one label from a note.
func (s *Store) RemoveLabel(ctx context.Context, projectID, noteID, name string) error {
	return s.write(ctx, projectID, noteID, deleteLabelSQL, noteID, name)
}

// write runs one label change and rewrites labels_json, holding the note's
// row lock from the start of the transaction.
func (s *Store) write(ctx context.Context, projectID, noteID, query string, args ...any) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	if err := tx.QueryRowContext(ctx, lockNoteSQL, noteID, projectID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, syncLabelsSQL, noteID); err != nil {
		return err
	}
	return tx.Commit()
}
