package db

import (
	"path/filepath"
	"testing"
)

const topicURL = "https://baatplassen.no/i/topic/1-a/"

func connectTemp(t *testing.T) Execer {
	t.Helper()
	Path = filepath.Join(t.TempDir(), "archive.db")
	conn, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestSitemapChangesToATopic(t *testing.T) {
	cases := []struct {
		name               string
		status             string
		nextPage, numPages int
		lastmod            string
		wantStatus         string
		wantNextPage       int
	}{
		{"unchanged done topic stays done", "done", 3, 10, "old", "done", 3},
		{"changed done topic resumes from its last page", "done", 9, 10, "new", "pending", 10},
		{"errored topic retries the failed page", "error", 5, 10, "old", "pending", 5},
		{"interrupted topic keeps its progress", "pending", 5, 10, "new", "pending", 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			Path = filepath.Join(t.TempDir(), "archive.db")
			conn, err := Connect()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			old := "old"
			if err := UpsertTopicFromSitemap(conn, 1, topicURL, &old); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec("UPDATE topics SET status = ?, next_page = ?, num_pages = ?", c.status, c.nextPage, c.numPages); err != nil {
				t.Fatal(err)
			}
			if err := UpsertTopicFromSitemap(conn, 1, topicURL, &c.lastmod); err != nil {
				t.Fatal(err)
			}
			var status string
			var nextPage int
			if err := conn.QueryRow("SELECT status, next_page FROM topics").Scan(&status, &nextPage); err != nil {
				t.Fatal(err)
			}
			if status != c.wantStatus || nextPage != c.wantNextPage {
				t.Errorf("got (%s, %d), want (%s, %d)", status, nextPage, c.wantStatus, c.wantNextPage)
			}
		})
	}
}

func TestLastmodMissingBothTimesIsNoChange(t *testing.T) {
	conn := connectTemp(t)
	if err := UpsertTopicFromSitemap(conn, 1, topicURL, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("UPDATE topics SET status = 'done'"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertTopicFromSitemap(conn, 1, topicURL, nil); err != nil {
		t.Fatal(err)
	}
	readonly, err := ConnectReadonly()
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	var status string
	if err := readonly.QueryRow("SELECT status FROM topics").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Errorf("status = %s, want done: NULL to NULL is no change", status)
	}
}

func TestReadonlyConnectionCannotWrite(t *testing.T) {
	connectTemp(t)
	readonly, err := ConnectReadonly()
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, err := readonly.Exec("DELETE FROM topics"); err == nil {
		t.Error("write succeeded on a read-only connection")
	}
}

func TestReadonlyWithoutArchiveExplainsWhatToDo(t *testing.T) {
	Path = filepath.Join(t.TempDir(), "missing.db")
	if _, err := ConnectReadonly(); err == nil {
		t.Error("no error for a missing archive")
	}
}

func TestPragmas(t *testing.T) {
	connectTemp(t)
	conn, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var journal string
	var synchronous, busy int
	if err := conn.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || synchronous != 1 || busy != 30000 {
		t.Errorf("journal=%s synchronous=%d busy=%d", journal, synchronous, busy)
	}
}
