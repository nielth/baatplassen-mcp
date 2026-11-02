"""Polite, resumable crawler: one request at a time, progress committed after each page."""

import re
import sqlite3
import time
from datetime import UTC, datetime

import httpx

from baatplassen import db
from baatplassen.parse import ParseError, parse_sitemap, parse_topic_page

SITEMAP_URL = "https://baatplassen.no/i/sitemap.php"
USER_AGENT = "baatplassen-personal-archive/0.1"

# Disallowed by name in https://baatplassen.no/robots.txt
ROBOTS_EXCLUDED_TOPICS = {179874, 179667}

RETRY_WAITS_SECONDS = [30, 60, 120, 300, 600, 900]
MAX_CONSECUTIVE_PARSE_ERRORS = 20
GONE_STATUSES = {403, 404, 410}
RETRY_STATUSES = {408, 425, 429, 500, 502, 503, 504, 520, 522, 524}


class CrawlAborted(Exception):
    """The site looks unhealthy or has changed. Rerunning resumes where this stopped."""


def log(message: str) -> None:
    print(f"{datetime.now():%Y-%m-%d %H:%M:%S} {message}", flush=True)


class Fetcher:
    """HTTP client that spaces requests out and backs off when the server struggles."""

    def __init__(self, delay: float) -> None:
        self.delay = delay
        self.last_request = 0.0
        self.requests = 0
        self.client = httpx.Client(
            headers={"User-Agent": USER_AGENT},
            follow_redirects=True,
            timeout=60,
        )

    def get(self, url: str) -> httpx.Response:
        for wait in [*RETRY_WAITS_SECONDS, None]:
            time.sleep(max(0.0, self.last_request + self.delay - time.monotonic()))
            self.last_request = time.monotonic()
            self.requests += 1
            problem: str
            retry_after = 0
            try:
                response = self.client.get(url)
            except httpx.TransportError as error:
                problem = repr(error)
            else:
                if response.status_code not in RETRY_STATUSES:
                    return response
                problem = f"HTTP {response.status_code}"
                header = response.headers.get("Retry-After", "")
                retry_after = int(header) if header.isdigit() else 0
            if wait is None:
                raise CrawlAborted(f"Giving up on {url}: {problem}")
            pause = max(wait, retry_after)
            log(f"{problem} for {url}, waiting {pause}s")
            time.sleep(pause)
        raise AssertionError("unreachable")


def topic_id_from_url(url: str) -> int | None:
    match = re.search(r"/topic/(\d+)-", url)
    return int(match.group(1)) if match else None


def discover(conn: sqlite3.Connection, delay: float) -> None:
    """Read every topic sitemap and queue new or changed topics."""
    fetcher = Fetcher(delay)
    index = parse_sitemap(fetcher.get(SITEMAP_URL).raise_for_status().content)
    sitemaps = [url for url, _ in index if "sitemap_content_forums_Topic" in url]
    if not sitemaps:
        raise CrawlAborted("No topic sitemaps found in the sitemap index")
    log(f"Reading {len(sitemaps)} topic sitemaps")
    for number, sitemap in enumerate(sitemaps, start=1):
        entries = parse_sitemap(fetcher.get(sitemap).raise_for_status().content)
        for url, lastmod in entries:
            topic_id = topic_id_from_url(url)
            if topic_id is not None and topic_id not in ROBOTS_EXCLUDED_TOPICS:
                db.upsert_topic_from_sitemap(conn, topic_id, url, lastmod)
        conn.commit()
        if number % 25 == 0:
            log(f"Read {number}/{len(sitemaps)} sitemaps")
    pending = conn.execute(
        "SELECT COUNT(*) FROM topics WHERE status = 'pending'"
    ).fetchone()[0]
    total = conn.execute("SELECT COUNT(*) FROM topics").fetchone()[0]
    log(f"{total} topics known, {pending} to crawl")


def finish_topic(
    conn: sqlite3.Connection, topic_id: int, status: str, error: str | None = None
) -> None:
    conn.execute(
        "UPDATE topics SET status = ?, error = ?, crawled_at = ? WHERE id = ?",
        (status, error, datetime.now(UTC).isoformat(timespec="seconds"), topic_id),
    )
    conn.commit()


def crawl_topic(
    conn: sqlite3.Connection, fetcher: Fetcher, topic_id: int, url: str, start: int
) -> bool:
    """Crawl a topic from the given page. Returns False if a page could not be parsed."""
    base = url.rstrip("/") + "/"
    page_number = start
    while True:
        page_url = base if page_number == 1 else f"{base}page/{page_number}/"
        response = fetcher.get(page_url)
        if response.status_code in GONE_STATUSES:
            finish_topic(conn, topic_id, "gone", f"HTTP {response.status_code}")
            return True
        if response.status_code != 200:
            finish_topic(conn, topic_id, "error", f"HTTP {response.status_code}")
            return True

        final_url = str(response.url)
        if topic_id_from_url(final_url) != topic_id:
            # Merged into another topic, which has its own sitemap entry.
            finish_topic(conn, topic_id, "gone", f"Redirected to {final_url}")
            return True
        # Asking past the last page redirects to the last page.
        found = re.search(r"/page/(\d+)/", final_url)
        actual_page = int(found.group(1)) if found else 1

        try:
            page = parse_topic_page(response.text)
        except ParseError as error:
            finish_topic(conn, topic_id, "error", f"{page_url}: {error}")
            log(f"Could not parse {page_url}: {error}")
            return False

        db.save_page(conn, topic_id, actual_page, page)
        if actual_page >= page.num_pages:
            finish_topic(conn, topic_id, "done")
            return True
        page_number = actual_page + 1
        conn.execute(
            "UPDATE topics SET next_page = ? WHERE id = ?", (page_number, topic_id)
        )
        conn.commit()


def crawl(conn: sqlite3.Connection, delay: float, max_topics: int | None) -> None:
    """Crawl queued topics, newest first, until none are left."""
    fetcher = Fetcher(delay)
    started = time.monotonic()
    crawled = 0
    parse_errors = 0
    while max_topics is None or crawled < max_topics:
        row = conn.execute("""
            SELECT id, url, next_page FROM topics
            WHERE status = 'pending' ORDER BY id DESC LIMIT 1
            """).fetchone()
        if row is None:
            log("Nothing left to crawl")
            break
        if crawl_topic(conn, fetcher, row["id"], row["url"], row["next_page"]):
            parse_errors = 0
        else:
            parse_errors += 1
            if parse_errors >= MAX_CONSECUTIVE_PARSE_ERRORS:
                raise CrawlAborted(
                    f"{parse_errors} unparseable topics in a row. The site may be "
                    "blocking the crawler or have changed its layout."
                )
        crawled += 1
        if crawled % 100 == 0:
            remaining = conn.execute(
                "SELECT COUNT(*) FROM topics WHERE status = 'pending'"
            ).fetchone()[0]
            rate = fetcher.requests / (time.monotonic() - started)
            log(
                f"{crawled} topics, {fetcher.requests} requests this run "
                f"({rate:.2f}/s), {remaining} topics left"
            )
    log(f"Stopped after {crawled} topics and {fetcher.requests} requests")
