"""SQLite storage. Post text lives in the FTS5 table, so it is not stored twice."""

import os
import sqlite3
from pathlib import Path

from baatplassen.parse import TopicPage

DB_PATH = Path(
    os.environ.get(
        "BAATPLASSEN_DB",
        Path(__file__).resolve().parents[2] / "data" / "baatplassen.db",
    )
)

# Diacritics are kept: with them removed, "båt" would also match "bat".
SCHEMA = """
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
"""


def connect() -> sqlite3.Connection:
    """Open the database for writing, creating it if needed."""
    DB_PATH.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(DB_PATH, timeout=30)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode = WAL")
    conn.execute("PRAGMA synchronous = NORMAL")
    conn.executescript(SCHEMA)
    return conn


def connect_readonly() -> sqlite3.Connection:
    """Open the database for searching. Safe to use while a crawl is running."""
    if not DB_PATH.exists():
        raise FileNotFoundError(
            f"No archive at {DB_PATH}. Run `uv run baatplassen discover` and "
            "`uv run baatplassen crawl` first."
        )
    conn = sqlite3.connect(f"file:{DB_PATH}?mode=ro", uri=True, timeout=30)
    conn.row_factory = sqlite3.Row
    return conn


def upsert_topic_from_sitemap(
    conn: sqlite3.Connection, topic_id: int, url: str, lastmod: str | None
) -> None:
    """Add a topic, or queue it again from its last known page if it has changed."""
    conn.execute(
        """
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
        """,
        (topic_id, url, lastmod),
    )


def save_page(
    conn: sqlite3.Connection, topic_id: int, page_number: int, page: TopicPage
) -> None:
    """Store one page's posts. Not committed: the caller commits with the progress."""
    for post in page.posts:
        conn.execute("DELETE FROM posts_fts WHERE rowid = ?", (post.id,))
        conn.execute(
            "INSERT INTO posts_fts (rowid, title, body, full_text) VALUES (?, ?, ?, ?)",
            (post.id, page.title, post.body, post.full_text),
        )
        conn.execute(
            """
            INSERT OR REPLACE INTO posts (id, topic_id, page, author, author_id, posted_at)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            (
                post.id,
                topic_id,
                page_number,
                post.author,
                post.author_id,
                post.posted_at,
            ),
        )
    conn.execute(
        """
        UPDATE topics
        SET title = ?, forum_id = ?, forum = ?, num_pages = ?, expected_posts = ?
        WHERE id = ?
        """,
        (
            page.title,
            page.forum_id,
            page.forum,
            page.num_pages,
            page.expected_posts,
            topic_id,
        ),
    )
