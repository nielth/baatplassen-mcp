"""Command line entry point."""

import argparse
import json

from baatplassen import crawl, db, search


def main() -> None:
    parser = argparse.ArgumentParser(prog="baatplassen", description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)

    discover = commands.add_parser("discover", help="queue new and changed topics")
    discover.add_argument("--delay", type=float, default=1.0)

    run = commands.add_parser("crawl", help="download queued topics")
    run.add_argument(
        "--delay", type=float, default=1.0, help="seconds between requests"
    )
    run.add_argument("--max-topics", type=int, help="stop after this many topics")

    commands.add_parser("status", help="show crawl coverage")

    find = commands.add_parser("search", help="search from the terminal")
    find.add_argument("query")
    find.add_argument("--limit", type=int, default=10)

    words = commands.add_parser("words", help="list words the forum uses")
    words.add_argument("pattern")

    serve = commands.add_parser("serve", help="run the MCP server for Claude")
    serve.add_argument("--http", action="store_true", help="serve over HTTP")

    args = parser.parse_args()
    if args.command == "discover":
        crawl.discover(db.connect(), args.delay)
    elif args.command == "crawl":
        crawl.crawl(db.connect(), args.delay, args.max_topics)
    elif args.command == "status":
        status = search.archive_status(db.connect_readonly())
        print(json.dumps(status, indent=2, ensure_ascii=False))
    elif args.command == "search":
        results = search.search_posts(
            db.connect_readonly(), args.query, limit=args.limit
        )
        print(json.dumps(results, indent=2, ensure_ascii=False))
    elif args.command == "words":
        found = search.find_words(db.connect_readonly(), args.pattern)
        print(json.dumps(found, indent=2, ensure_ascii=False))
    elif args.command == "serve":
        from baatplassen import server

        if args.http:
            server.serve_http()
        else:
            server.serve()
