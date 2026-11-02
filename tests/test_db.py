import sqlite3

import pytest

from baatplassen import db

URL = "https://baatplassen.no/i/topic/1-a/"


@pytest.fixture
def conn() -> sqlite3.Connection:
    connection = sqlite3.connect(":memory:")
    connection.executescript(db.SCHEMA)
    return connection


def set_topic(
    conn: sqlite3.Connection, status: str, next_page: int, num_pages: int
) -> None:
    db.upsert_topic_from_sitemap(conn, 1, URL, "old")
    conn.execute(
        "UPDATE topics SET status = ?, next_page = ?, num_pages = ?",
        (status, next_page, num_pages),
    )


def state_after_sitemap(conn: sqlite3.Connection, lastmod: str) -> tuple[str, int]:
    db.upsert_topic_from_sitemap(conn, 1, URL, lastmod)
    row = conn.execute("SELECT status, next_page FROM topics").fetchone()
    return row[0], row[1]


def test_unchanged_done_topic_stays_done(conn: sqlite3.Connection) -> None:
    set_topic(conn, "done", 3, 10)
    assert state_after_sitemap(conn, "old") == ("done", 3)


def test_changed_done_topic_resumes_from_its_last_page(
    conn: sqlite3.Connection,
) -> None:
    set_topic(conn, "done", 9, 10)
    assert state_after_sitemap(conn, "new") == ("pending", 10)


def test_errored_topic_retries_the_failed_page(conn: sqlite3.Connection) -> None:
    set_topic(conn, "error", 5, 10)
    assert state_after_sitemap(conn, "old") == ("pending", 5)


def test_interrupted_topic_keeps_its_progress(conn: sqlite3.Connection) -> None:
    set_topic(conn, "pending", 5, 10)
    assert state_after_sitemap(conn, "new") == ("pending", 5)
