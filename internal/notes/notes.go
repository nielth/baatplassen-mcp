// Package notes is the owner's own log about their boat: dated text with optional photos.
// It is kept in its own database, apart from the forum archive, because it can't be
// downloaded again.
package notes

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nielth/baatplassen-mcp/internal/db"
)

const timeFormat = "2006-01-02T15:04:05Z"

const schema = `
CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    text TEXT NOT NULL,
    deleted_at TEXT  -- Deleted notes are kept, so a mistaken delete can be undone.
);
CREATE TABLE IF NOT EXISTS note_images (
    id INTEGER PRIMARY KEY,
    note_id INTEGER NOT NULL REFERENCES notes (id),
    jpeg BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS note_images_note ON note_images (note_id, id);
`

// Note is one log entry. Images is how many photos it has.
type Note struct {
	ID        int64  `json:"note_id"`
	CreatedAt string `json:"created_at"`
	Text      string `json:"text"`
	Images    int    `json:"images"`
}

// Path is where the notes live: next to the archive.
func Path() string {
	return filepath.Join(filepath.Dir(db.Path), "notes.db")
}

// Connect opens the notes, creating them if needed.
func Connect() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return nil, err
	}
	conn, err := db.Open(Path(), "&_journal_mode=WAL&_synchronous=FULL")
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// Add saves a note with its photos, already converted to JPEG.
func Add(conn *sql.DB, text string, jpegs [][]byte) (Note, error) {
	note := Note{CreatedAt: time.Now().UTC().Format(timeFormat), Text: text, Images: len(jpegs)}
	tx, err := conn.Begin()
	if err != nil {
		return note, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO notes (created_at, text) VALUES (?, ?)", note.CreatedAt, text)
	if err != nil {
		return note, err
	}
	if note.ID, err = result.LastInsertId(); err != nil {
		return note, err
	}
	for _, jpeg := range jpegs {
		if _, err := tx.Exec("INSERT INTO note_images (note_id, jpeg) VALUES (?, ?)", note.ID, jpeg); err != nil {
			return note, err
		}
	}
	return note, tx.Commit()
}

// Search returns the notes containing every word of the query, newest first. An empty query
// returns all notes. There are few enough notes to simply check each one.
func Search(conn *sql.DB, query string, limit int) ([]Note, error) {
	words := []string{}
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if word = strings.Trim(word, `"*`); word != "" {
			words = append(words, word)
		}
	}
	rows, err := conn.Query(`
        SELECT id, created_at, text,
               (SELECT COUNT(*) FROM note_images WHERE note_id = notes.id)
        FROM notes
        WHERE deleted_at IS NULL
        ORDER BY id DESC
        `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []Note{}
	for rows.Next() && len(found) < limit {
		var note Note
		if err := rows.Scan(&note.ID, &note.CreatedAt, &note.Text, &note.Images); err != nil {
			return nil, err
		}
		if containsAll(strings.ToLower(note.Text), words) {
			found = append(found, note)
		}
	}
	return found, rows.Err()
}

func containsAll(text string, words []string) bool {
	for _, word := range words {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

// Get returns a note, or nil if there is none with that id.
func Get(conn *sql.DB, id int64) (*Note, error) {
	notes, err := conn.Query(`
        SELECT id, created_at, text,
               (SELECT COUNT(*) FROM note_images WHERE note_id = notes.id)
        FROM notes
        WHERE id = ? AND deleted_at IS NULL
        `, id)
	if err != nil {
		return nil, err
	}
	defer notes.Close()
	if !notes.Next() {
		return nil, notes.Err()
	}
	var note Note
	if err := notes.Scan(&note.ID, &note.CreatedAt, &note.Text, &note.Images); err != nil {
		return nil, err
	}
	return &note, nil
}

// Delete hides a note. It reports false if there was no such note.
func Delete(conn *sql.DB, id int64) (bool, error) {
	result, err := conn.Exec(
		"UPDATE notes SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL",
		time.Now().UTC().Format(timeFormat), id,
	)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

// ImageIDs returns the ids of a note's photos, in the order they were added.
func ImageIDs(conn *sql.DB, noteID int64) ([]int64, error) {
	rows, err := conn.Query("SELECT id FROM note_images WHERE note_id = ? ORDER BY id", noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Image returns a photo as JPEG, or nil if there is none with that id or its note is deleted.
func Image(conn *sql.DB, id int64) ([]byte, error) {
	var jpeg []byte
	err := conn.QueryRow(`
        SELECT jpeg FROM note_images
        JOIN notes ON notes.id = note_images.note_id
        WHERE note_images.id = ? AND notes.deleted_at IS NULL
        `, id).Scan(&jpeg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return jpeg, err
}
