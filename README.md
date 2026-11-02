# baatplassen

A personal, searchable archive of the [baatplassen.no forum](https://baatplassen.no/i/) that
Claude can search and answer from. Posts are stored in a local SQLite full-text index and exposed
through an MCP server. Claude runs the searches itself, so no API key or embeddings are needed.

## Using it

Run `claude` in this directory and approve the `baatplassen` MCP server the first time. Then ask:

> Hva sier folk om å bytte impeller på Volvo Penta MD2030?

It works during a crawl too, but only over what has been downloaded so far (newest topics first).

## Crawling

```sh
uv run baatplassen discover   # read the sitemaps and queue new or changed topics
uv run baatplassen crawl      # download queued topics, one request per second
uv run baatplassen status     # show coverage
```

Progress is saved after every page, so a stopped crawl can simply be started again. To run it in
the background and keep the Mac awake:

```sh
nohup caffeinate -i uv run baatplassen crawl >> data/crawl.log 2>&1 &
tail -f data/crawl.log
pkill -f "baatplassen crawl"   # stop
```

Run `discover` and then `crawl` again later to fetch new posts. Changed topics are read from their
last known page, so this is quick, but edits to older pages and deleted posts are not picked up.

## Limits

- Only what a logged-out visitor can see is archived.
- Images are fetched only when Claude asks to look at one, then cached resized in `data/images.db`.
  Downloading them all up front would be about 100 GB or more and take days of extra requests.
- Images in `/bildearkiv` (mostly before about 2015, disallowed by robots.txt) and on other sites
  are not fetched. Claude gets the link instead.
- Search matches words, not meaning, and has no Norwegian stemming. `motor*` finds `motoren` but
  not `dieselmotor`. Claude compensates by thinking up variants and using `find_words` to see which
  words the forum really uses.
- The crawler follows robots.txt, including the two topics it excludes by name.
- Posts belong to their authors. The archive is for personal use, so don't republish it.

## Development

```sh
uv run pytest && uv run black . && uv run ruff check . && uv run mypy src tests
```
