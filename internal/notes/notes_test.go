package notes

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nielth/baatplassen-mcp/internal/db"
)

func tempNotes(t *testing.T) *sql.DB {
	t.Helper()
	db.Path = filepath.Join(t.TempDir(), "archive.db")
	conn, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func ids(found []Note) []int64 {
	result := []int64{}
	for _, note := range found {
		result = append(result, note.ID)
	}
	return result
}

func TestNotesAreKeptApartFromTheArchive(t *testing.T) {
	tempNotes(t)
	if Path() != filepath.Join(filepath.Dir(db.Path), "notes.db") {
		t.Errorf("path = %s", Path())
	}
}

func TestSearchFindsEveryWordIgnoringCaseNewestFirst(t *testing.T) {
	conn := tempNotes(t)
	for _, text := range []string{
		"Motoren er Yanmar 3GM30F",
		"Byttet impeller, Johnson 09-1027B",
		"Nytt IMPELLER til Yanmar kjøpt, ikke montert",
	} {
		if _, err := Add(conn, text, nil); err != nil {
			t.Fatal(err)
		}
	}
	for query, want := range map[string][]int64{
		"impeller":          {3, 2},
		"Impeller* yanmar":  {3},
		`"yanmar"`:          {3, 1},
		"":                  {3, 2, 1},
		"KJØPT":             {3},
		"finnes ikke":       {},
		"impeller johnson ": {2},
	} {
		found, err := Search(conn, query, 50)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(found); !slices.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", query, got, want)
		}
	}
	found, err := Search(conn, "", 2)
	if err != nil || len(found) != 2 {
		t.Errorf("limit ignored: %v %v", found, err)
	}
}

func TestPhotosAreStoredWithTheirNote(t *testing.T) {
	conn := tempNotes(t)
	note, err := Add(conn, "Typeskilt", [][]byte{[]byte("first"), []byte("second")})
	if err != nil {
		t.Fatal(err)
	}
	if note.ID != 1 || note.Images != 2 || note.CreatedAt == "" {
		t.Errorf("note = %+v", note)
	}
	imageIDs, err := ImageIDs(conn, note.ID)
	if err != nil || len(imageIDs) != 2 {
		t.Fatalf("ids = %v %v", imageIDs, err)
	}
	jpeg, err := Image(conn, imageIDs[1])
	if err != nil || string(jpeg) != "second" {
		t.Errorf("image = %q %v", jpeg, err)
	}
	stored, err := Get(conn, note.ID)
	if err != nil || stored == nil || stored.Images != 2 || stored.Text != "Typeskilt" {
		t.Errorf("get = %+v %v", stored, err)
	}
}

func TestDeletedNotesAndTheirPhotosAreHiddenButKept(t *testing.T) {
	conn := tempNotes(t)
	note, err := Add(conn, "Feil", [][]byte{[]byte("photo")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false} {
		deleted, err := Delete(conn, note.ID)
		if err != nil || deleted != want {
			t.Errorf("delete = %v %v, want %v", deleted, err, want)
		}
	}
	if found, _ := Search(conn, "", 50); len(found) != 0 {
		t.Errorf("found = %v", found)
	}
	if stored, _ := Get(conn, note.ID); stored != nil {
		t.Errorf("get = %+v", stored)
	}
	if jpeg, _ := Image(conn, 1); jpeg != nil {
		t.Errorf("image of deleted note served")
	}
	var kept int
	if err := conn.QueryRow("SELECT COUNT(*) FROM notes WHERE deleted_at IS NOT NULL").Scan(&kept); err != nil || kept != 1 {
		t.Errorf("kept = %d %v", kept, err)
	}
}

func TestMissingNoteAndImage(t *testing.T) {
	conn := tempNotes(t)
	if note, err := Get(conn, 9); note != nil || err != nil {
		t.Errorf("get = %v %v", note, err)
	}
	if jpeg, err := Image(conn, 9); jpeg != nil || err != nil {
		t.Errorf("image = %v %v", jpeg, err)
	}
	if deleted, err := Delete(conn, 9); deleted || err != nil {
		t.Errorf("delete = %v %v", deleted, err)
	}
}
