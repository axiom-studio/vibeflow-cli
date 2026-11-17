// Package tags keeps key/value tags on memories in PostgreSQL. The tags
// table is the source of truth; memories.tags_json is the copy reads and
// recall filtering use.
package tags

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNotFound is returned for a memory that does not exist in the workspace.
var ErrNotFound = errors.New("tags: memory not found")

// Schema is the part of the database this package uses.
const Schema = `
CREATE TABLE memories (
	id           text PRIMARY KEY,
	workspace_id text NOT NULL,
	content      text NOT NULL,
	tags_json    jsonb
);
CREATE TABLE tags (
	memory_id text NOT NULL REFERENCES memories(id),
	key       text NOT NULL,
	value     text NOT NULL,
	PRIMARY KEY (memory_id, key)
);`

const insertTagSQL = `
INSERT INTO tags(memory_id, key, value)
SELECT $1, $2, $3
WHERE EXISTS (SELECT 1 FROM memories WHERE id = $1 AND workspace_id = $4)
ON CONFLICT (memory_id, key) DO UPDATE SET value = excluded.value`

const deleteTagSQL = `
DELETE FROM tags WHERE memory_id = $1 AND key = $2
AND EXISTS (SELECT 1 FROM memories WHERE id = $1 AND workspace_id = $3)`

// syncTagsSQL rewrites a memory's tags_json from its tag rows (NULL when it
// has none).
const syncTagsSQL = `
UPDATE memories SET tags_json = (SELECT jsonb_object_agg(key, value) FROM tags WHERE memory_id = $1)
WHERE id = $1`

// Store reads and writes tags.
type Store struct {
	DB *sql.DB
}

// UpsertTag sets one tag on a memory. tags_json is rewritten from the tag
// rows in the same transaction, so reads and recall see the new tag.
func (s *Store) UpsertTag(ctx context.Context, workspaceID, memoryID, key, value string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, insertTagSQL, memoryID, key, value, workspaceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, syncTagsSQL, memoryID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteTag removes one tag from a memory and rewrites tags_json to match.
func (s *Store) DeleteTag(ctx context.Context, workspaceID, memoryID, key string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, deleteTagSQL, memoryID, key, workspaceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := tx.ExecContext(ctx, syncTagsSQL, memoryID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Memory is a memory as reads return it.
type Memory struct {
	ID       string
	Content  string
	TagsJSON []byte
}

// GetMemory returns one memory with its tags_json.
func (s *Store) GetMemory(ctx context.Context, workspaceID, memoryID string) (Memory, error) {
	var m Memory
	err := s.DB.QueryRowContext(ctx, `SELECT id, content, tags_json FROM memories WHERE id = $1 AND workspace_id = $2`, memoryID, workspaceID).
		Scan(&m.ID, &m.Content, &m.TagsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}
