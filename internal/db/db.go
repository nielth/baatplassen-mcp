// Package db is the SQLite storage. Post text lives in the FTS5 table, so it is not stored twice.
package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	// Registers the "sqlite3" driver. Needs the sqlite_fts5 build tag.
	_ "github.com/mattn/go-sqlite3"

	"github.com/nielth/baatplassen-mcp/internal/parse"
)

// Path is the archive's location: BAATPLASSEN_DB, or data/baatplassen.db in the current folder.
var Path = defaultPath()

func defaultPath() string {
	if path := os.Getenv("BAATPLASSEN_DB"); path != "" {
		return path
	}
	return filepath.Join("data", "baatplassen.db")
}

// Schema creates the archive's tables. Diacritics are kept: with them removed, "båt" would
// also match "bat".
const Schema = `
CREATE TABLE IF NOT EXISTS topics (
    id INTEGER PRIMARY KEY,
    url TEXT NOT NULL,
    lastmod TEXT,
    title TEXT,
    forum_id INTEGER,
    forum TEXT,
    num_pages INTEGER,
    expected_posts INTEGER,
    next_page INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, done, gone, error
    error TEXT,
    crawled_at TEXT
);
CREATE INDEX IF NOT EXISTS topics_status ON topics (status, id);

CREATE TABLE IF NOT EXISTS posts (
    id INTEGER PRIMARY KEY,
    topic_id INTEGER NOT NULL REFERENCES topics (id),
    page INTEGER NOT NULL,
    author TEXT NOT NULL,
    author_id INTEGER,
    posted_at TEXT
);
CREATE INDEX IF NOT EXISTS posts_topic ON posts (topic_id, id);
CREATE INDEX IF NOT EXISTS posts_author ON posts (author COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS posts_fts USING fts5 (
    title,
    body,
    full_text UNINDEXED,
    tokenize = "unicode61 remove_diacritics 0"
);
`

// Execer is what the write functions need: a database or an open transaction.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Open opens a SQLite file with the given extra DSN parameters. One connection only: a
// transaction must never wait for a second one, and temp tables live on a connection.
func Open(path, params string) (*sql.DB, error) {
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?_busy_timeout=30000" + params
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	return conn, nil
}

// Connect opens the database for writing, creating it if needed.
func Connect() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(Path), 0o755); err != nil {
		return nil, err
	}
	conn, err := Open(Path, "&_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(Schema); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// ConnectReadonly opens the database for searching. Safe to use while a crawl is running.
func ConnectReadonly() (*sql.DB, error) {
	if _, err := os.Stat(Path); err != nil {
		return nil, fmt.Errorf("no archive at %s. Run `baatplassen discover` and `baatplassen crawl` first", Path)
	}
	return Open(Path, "&mode=ro")
}

// UpsertTopicFromSitemap adds a topic, or queues it again from its last known page if it
// has changed.
func UpsertTopicFromSitemap(conn Execer, topicID int64, topicURL string, lastmod *string) error {
	_, err := conn.Exec(`
        INSERT INTO topics (id, url, lastmod) VALUES (?, ?, ?)
        ON CONFLICT (id) DO UPDATE SET
            next_page = CASE
                WHEN status = 'done' AND lastmod IS NOT excluded.lastmod
                    THEN COALESCE(num_pages, 1)
                ELSE next_page
            END,
            status = CASE
                WHEN lastmod IS NOT excluded.lastmod OR status = 'error' THEN 'pending'
                ELSE status
            END,
            url = excluded.url,
            lastmod = excluded.lastmod
        `, topicID, topicURL, lastmod)
	return err
}

// SavePage stores one page's posts. Not committed: the caller commits with the progress.
func SavePage(conn Execer, topicID int64, pageNumber int, page *parse.TopicPage) error {
	for _, post := range page.Posts {
		if _, err := conn.Exec("DELETE FROM posts_fts WHERE rowid = ?", post.ID); err != nil {
			return err
		}
		if _, err := conn.Exec(
			"INSERT INTO posts_fts (rowid, title, body, full_text) VALUES (?, ?, ?, ?)",
			post.ID, page.Title, post.Body, post.FullText,
		); err != nil {
			return err
		}
		if _, err := conn.Exec(`
            INSERT OR REPLACE INTO posts (id, topic_id, page, author, author_id, posted_at)
            VALUES (?, ?, ?, ?, ?, ?)
            `, post.ID, topicID, pageNumber, post.Author, post.AuthorID, post.PostedAt,
		); err != nil {
			return err
		}
	}
	_, err := conn.Exec(`
        UPDATE topics
        SET title = ?, forum_id = ?, forum = ?, num_pages = ?, expected_posts = ?
        WHERE id = ?
        `, page.Title, page.ForumID, page.Forum, page.NumPages, page.ExpectedPosts, topicID)
	return err
}
