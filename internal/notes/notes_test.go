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
		if _, err := Add(conn, Admin, text, nil); err != nil {
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
		found, err := Search(conn, Admin, query, 50)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(found); !slices.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", query, got, want)
		}
	}
	found, err := Search(conn, Admin, "", 2)
	if err != nil || len(found) != 2 {
		t.Errorf("limit ignored: %v %v", found, err)
	}
}

func TestPhotosAreStoredWithTheirNote(t *testing.T) {
	conn := tempNotes(t)
	note, err := Add(conn, Admin, "Typeskilt", [][]byte{[]byte("first"), []byte("second")})
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
	jpeg, err := Image(conn, Admin, imageIDs[1])
	if err != nil || string(jpeg) != "second" {
		t.Errorf("image = %q %v", jpeg, err)
	}
	stored, err := Get(conn, Admin, note.ID)
	if err != nil || stored == nil || stored.Images != 2 || stored.Text != "Typeskilt" {
		t.Errorf("get = %+v %v", stored, err)
	}
}

func TestDeletedNotesAndTheirPhotosAreHiddenButKept(t *testing.T) {
	conn := tempNotes(t)
	note, err := Add(conn, Admin, "Feil", [][]byte{[]byte("photo")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false} {
		deleted, err := Delete(conn, Admin, note.ID)
		if err != nil || deleted != want {
			t.Errorf("delete = %v %v, want %v", deleted, err, want)
		}
	}
	if found, _ := Search(conn, Admin, "", 50); len(found) != 0 {
		t.Errorf("found = %v", found)
	}
	if stored, _ := Get(conn, Admin, note.ID); stored != nil {
		t.Errorf("get = %+v", stored)
	}
	if jpeg, _ := Image(conn, Admin, 1); jpeg != nil {
		t.Errorf("image of deleted note served")
	}
	var kept int
	if err := conn.QueryRow("SELECT COUNT(*) FROM notes WHERE deleted_at IS NOT NULL").Scan(&kept); err != nil || kept != 1 {
		t.Errorf("kept = %d %v", kept, err)
	}
}

func TestMissingNoteAndImage(t *testing.T) {
	conn := tempNotes(t)
	if note, err := Get(conn, Admin, 9); note != nil || err != nil {
		t.Errorf("get = %v %v", note, err)
	}
	if jpeg, err := Image(conn, Admin, 9); jpeg != nil || err != nil {
		t.Errorf("image = %v %v", jpeg, err)
	}
	if deleted, err := Delete(conn, Admin, 9); deleted || err != nil {
		t.Errorf("delete = %v %v", deleted, err)
	}
}

func TestUsersOnlySeeTheirOwnNotes(t *testing.T) {
	conn := tempNotes(t)
	kari, secret, err := CreateUser(conn, "Kari")
	if err != nil {
		t.Fatal(err)
	}
	mine, _ := Add(conn, Admin, "Min impeller", [][]byte{[]byte("mine")})
	hers, _ := Add(conn, kari.ID, "Kari sin impeller", [][]byte{[]byte("hers")})
	found, err := Search(conn, kari.ID, "impeller", 50)
	if err != nil || !slices.Equal(ids(found), []int64{hers.ID}) {
		t.Errorf("found = %v %v", found, err)
	}
	if note, _ := Get(conn, kari.ID, mine.ID); note != nil {
		t.Errorf("got the admin's note")
	}
	if jpeg, _ := Image(conn, kari.ID, 1); jpeg != nil {
		t.Errorf("got the admin's photo")
	}
	if deleted, _ := Delete(conn, kari.ID, mine.ID); deleted {
		t.Errorf("deleted the admin's note")
	}
	if found, _ := Search(conn, Admin, "", 50); !slices.Equal(ids(found), []int64{mine.ID}) {
		t.Errorf("admin found = %v", found)
	}
	if id, ok, err := UserBySecret(conn, secret); !ok || err != nil || id != kari.ID {
		t.Errorf("user = %d %v %v", id, ok, err)
	}
}

func TestSecretsAreRandomAndOnlyTheirHashIsStored(t *testing.T) {
	conn := tempNotes(t)
	_, first, _ := CreateUser(conn, "A")
	_, second, _ := CreateUser(conn, "B")
	if len(first) != 64 || first == second {
		t.Errorf("secrets %q %q", first, second)
	}
	var stored int
	conn.QueryRow("SELECT COUNT(*) FROM users WHERE secret_hash = ?", first).Scan(&stored)
	if stored != 0 {
		t.Errorf("secret stored in plain text")
	}
	if _, ok, _ := UserBySecret(conn, "wrong"); ok {
		t.Errorf("wrong secret accepted")
	}
}

func TestRevokedUserLosesAccessButKeepsNotes(t *testing.T) {
	conn := tempNotes(t)
	user, secret, _ := CreateUser(conn, "Kari")
	Add(conn, user.ID, "Notat", nil)
	if err := RevokeUser(conn, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := UserBySecret(conn, secret); ok {
		t.Errorf("revoked secret accepted")
	}
	if users, _ := Users(conn); len(users) != 0 {
		t.Errorf("users = %v", users)
	}
	if found, _ := Search(conn, user.ID, "", 50); len(found) != 1 {
		t.Errorf("notes lost: %v", found)
	}
}

func TestNotesFromBeforeUsersBelongToTheAdmin(t *testing.T) {
	db.Path = filepath.Join(t.TempDir(), "archive.db")
	old, err := db.Open(Path(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
        CREATE TABLE notes (id INTEGER PRIMARY KEY, created_at TEXT NOT NULL, text TEXT NOT NULL, deleted_at TEXT);
        INSERT INTO notes (created_at, text) VALUES ('2026-11-05T10:00:00Z', 'Gammelt notat');
        `); err != nil {
		t.Fatal(err)
	}
	old.Close()
	conn, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if found, _ := Search(conn, Admin, "gammelt", 50); len(found) != 1 {
		t.Errorf("found = %v", found)
	}
}
