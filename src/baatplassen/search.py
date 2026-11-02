"""Queries behind both the MCP tools and the CLI."""

import re
import sqlite3
from typing import Any


def _post_url(topic_url: str, post_id: int) -> str:
    return f"{topic_url}?do=findComment&comment={post_id}"


def _quote_terms(query: str) -> str:
    """Turn free text into valid FTS5 syntax, keeping a trailing * as a prefix search."""
    terms = []
    for term in query.split():
        prefix = term.endswith("*")
        cleaned = term.rstrip("*").replace('"', "")
        if cleaned:
            terms.append(f'"{cleaned}"' + ("*" if prefix else ""))
    return " ".join(terms)


def search_posts(
    conn: sqlite3.Connection,
    query: str,
    forum: str | None = None,
    author: str | None = None,
    since: str | None = None,
    until: str | None = None,
    limit: int = 20,
    offset: int = 0,
    max_per_topic: int = 3,
) -> list[dict[str, Any]]:
    """Rank posts by relevance, with title matches weighted above body matches."""
    filters = ["posts_fts MATCH :query"]
    params: dict[str, Any] = {
        "forum": f"%{forum}%",
        "author": author,
        "since": since,
        "until": until,
        "limit": limit,
        "offset": offset,
        "max_per_topic": max_per_topic,
    }
    if forum:
        filters.append("t.forum LIKE :forum")
    if author:
        filters.append("p.author = :author COLLATE NOCASE")
    if since:
        filters.append("p.posted_at >= :since")
    if until:
        filters.append("p.posted_at < :until")

    sql = f"""
        WITH ranked AS (
            SELECT
                p.id AS post_id, p.topic_id, p.author, p.posted_at,
                t.title, t.forum, t.url,
                bm25(posts_fts, 5.0, 1.0, 0.0) AS score
            FROM posts_fts
            JOIN posts p ON p.id = posts_fts.rowid
            JOIN topics t ON t.id = p.topic_id
            WHERE {" AND ".join(filters)}
        ),
        limited AS (
            SELECT *, ROW_NUMBER() OVER (PARTITION BY topic_id ORDER BY score) AS topic_rank
            FROM ranked
        )
        SELECT * FROM limited
        WHERE topic_rank <= :max_per_topic
        ORDER BY score
        LIMIT :limit OFFSET :offset
    """
    try:
        rows = conn.execute(sql, {**params, "query": query}).fetchall()
    except sqlite3.OperationalError as error:
        # e.g. "volvo-penta" is invalid FTS5 syntax. Retry treating terms literally.
        if not re.search(r"fts5|no such column|syntax error", str(error)):
            raise
        query = _quote_terms(query)
        rows = conn.execute(sql, {**params, "query": query}).fetchall()

    results = []
    for row in rows:
        position = conn.execute(
            "SELECT COUNT(*) FROM posts WHERE topic_id = ? AND id < ?",
            (row["topic_id"], row["post_id"]),
        ).fetchone()[0]
        # Built per result: doing this in the ranking query makes common terms very slow.
        snippet = conn.execute(
            """
            SELECT snippet(posts_fts, 1, '«', '»', ' … ', 48) FROM posts_fts
            WHERE posts_fts MATCH ? AND rowid = ?
            """,
            (query, row["post_id"]),
        ).fetchone()[0]
        results.append(
            {
                "post_id": row["post_id"],
                "topic_id": row["topic_id"],
                "topic_title": row["title"],
                "forum": row["forum"],
                "author": row["author"],
                "posted_at": row["posted_at"],
                "position_in_topic": position,
                "snippet": snippet,
                "url": _post_url(row["url"], row["post_id"]),
            }
        )
    return results


def find_words(
    conn: sqlite3.Connection, pattern: str, limit: int = 50
) -> list[dict[str, Any]]:
    """List words used on the forum that match a pattern, most widely used first.

    A pattern without wildcards matches anywhere in a word. This scans the whole word
    list, so it takes several seconds on a full archive.
    """
    glob = pattern.lower()
    if not re.search(r"[*?\[]", glob):
        glob = f"*{glob}*"
    # A temp table, because the archive is opened read-only.
    conn.execute("""
        CREATE VIRTUAL TABLE IF NOT EXISTS temp.posts_vocab
        USING fts5vocab(main, posts_fts, 'row')
        """)
    rows = conn.execute(
        """
        SELECT term, doc FROM temp.posts_vocab
        WHERE term GLOB ? ORDER BY doc DESC, term LIMIT ?
        """,
        (glob, limit),
    ).fetchall()
    return [{"word": row[0], "posts": row[1]} for row in rows]


def get_topic(
    conn: sqlite3.Connection, topic_id: int, offset: int = 0, limit: int = 50
) -> dict[str, Any]:
    """Return a topic's posts in order, with quotes included."""
    topic = conn.execute(
        "SELECT id, title, forum, url, status FROM topics WHERE id = ?", (topic_id,)
    ).fetchone()
    if topic is None:
        return {"error": f"No topic with id {topic_id}"}
    total = conn.execute(
        "SELECT COUNT(*) FROM posts WHERE topic_id = ?", (topic_id,)
    ).fetchone()[0]
    rows = conn.execute(
        """
        SELECT p.id, p.author, p.posted_at, COALESCE(f.full_text, f.body) AS text
        FROM posts p JOIN posts_fts f ON f.rowid = p.id
        WHERE p.topic_id = ?
        ORDER BY p.id
        LIMIT ? OFFSET ?
        """,
        (topic_id, limit, offset),
    ).fetchall()
    return {
        "topic_id": topic["id"],
        "title": topic["title"],
        "forum": topic["forum"],
        "url": topic["url"],
        "crawl_status": topic["status"],
        "total_posts": total,
        "offset": offset,
        "posts": [
            {
                "post_id": row["id"],
                "author": row["author"],
                "posted_at": row["posted_at"],
                "text": row["text"],
                "url": _post_url(topic["url"], row["id"]),
            }
            for row in rows
        ],
    }


def archive_status(conn: sqlite3.Connection) -> dict[str, Any]:
    """Report crawl coverage and the forums available for filtering."""
    statuses = {
        row["status"]: row["n"]
        for row in conn.execute(
            "SELECT status, COUNT(*) AS n FROM topics GROUP BY status"
        )
    }
    posts = conn.execute(
        "SELECT COUNT(*) AS n, MIN(posted_at) AS oldest, MAX(posted_at) AS newest FROM posts"
    ).fetchone()
    incomplete = conn.execute("""
        SELECT COUNT(*) FROM (
            SELECT t.id FROM topics t JOIN posts p ON p.topic_id = t.id
            WHERE t.status = 'done' AND t.expected_posts IS NOT NULL
            GROUP BY t.id HAVING COUNT(*) < t.expected_posts
        )
        """).fetchone()[0]
    forums = conn.execute("""
        SELECT forum, COUNT(*) AS topics FROM topics
        WHERE forum IS NOT NULL GROUP BY forum ORDER BY topics DESC
        """).fetchall()
    return {
        "topics_by_status": statuses,
        "posts": posts["n"],
        "oldest_post": posts["oldest"],
        "newest_post": posts["newest"],
        "done_topics_with_fewer_posts_than_expected": incomplete,
        "forums": [{"forum": row["forum"], "topics": row["topics"]} for row in forums],
    }
