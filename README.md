# baatplassen

A personal, searchable archive of the [baatplassen.no forum](https://baatplassen.no/i/) that
Claude can search and answer from. Posts are stored in a local SQLite full-text index and exposed
through an MCP server. Claude runs the searches itself, so no API key or embeddings are needed.

## Using it

Run `claude` in this directory and approve the `baatplassen` MCP server the first time. Then ask:

> Hva sier folk om å bytte impeller på Volvo Penta MD2030?

It works during a crawl too, but only over what has been downloaded so far (newest topics first).

## Your own boat log

Tell Claude to note something and it saves it, dated, in `data/notes.db`:

> Noter at jeg byttet impeller i dag, Johnson 09-1027B. Motoren er Yanmar 3GM30F.

When you later ask about your own boat, Claude checks the log first and then searches the forum
for your actual engine and parts. Notes are your own and kept apart from the forum archive.

Claude can't pass photos from the chat to the server, so photos go through the notes page on the
server, `https://<your domain>/<secret>/notes`. It also lists everything in the log. Add it to
the phone's home screen. Photos are stored at most 1568 pixels on their longest side, which is
what Claude reads anyway, so keep the originals if you want full resolution.

Deleted notes are only hidden. To bring one back:

```sh
sqlite3 data/notes.db "UPDATE notes SET deleted_at = NULL WHERE id = 12"
```

Unlike the archive, the notes can't be downloaded again, so back them up:

```sh
sqlite3 data/notes.db ".backup notes-backup.db"
```

## Running it on a server

`docker compose up -d --build` starts two containers:

- `baatplassen-mcp` serves the archive and the notes page to a Claude connector. See the top of
  `compose.yaml` for the secret it needs in `.env`.
- `baatplassen-crawler` finishes the download and then looks for new posts every 24 hours. It waits
  an hour and tries again if the site is down.

Follow the crawler with `docker logs -f baatplassen-crawler`. Only one crawler can run at a time,
so a second one exits straight away.

## Crawling by hand

SQLite needs the `sqlite_fts5` build tag and a C compiler:

```sh
go build -tags sqlite_fts5 -o baatplassen ./cmd/baatplassen
./baatplassen discover   # read the sitemaps and queue new or changed topics
./baatplassen crawl      # download queued topics, one request per second
./baatplassen status     # show coverage
```

Progress is saved after every page, so a stopped crawl can simply be started again. To run it in
the background and keep the Mac awake:

```sh
nohup caffeinate -i ./baatplassen crawl >> data/crawl.log 2>&1 &
tail -f data/crawl.log
pkill -f "baatplassen crawl"   # stop
```

Run `discover` and then `crawl` again later to fetch new posts. Changed topics are read from their
last known page, so this is quick, but edits to older pages and deleted posts are not picked up.

## Limits

- Only what a logged-out visitor can see is archived.
- Images are fetched only when Claude asks to look at one, then cached resized in `data/images.db`.
  Downloading them all up front would be about 100 GB or more and take days of extra requests.
- Images on other sites are not fetched. Claude gets the link instead. Old images on the forum may
  also have been removed.
- Search matches words, not meaning, and has no Norwegian stemming. `motor*` finds `motoren` but
  not `dieselmotor`. Claude compensates by thinking up variants and using `find_words` to see which
  words the forum really uses.
- The crawler follows robots.txt, including the two topics it excludes by name. The exception is
  `/bildearkiv` images, which Claude may fetch with permission from the site's owner.
- Posts belong to their authors. The archive is for personal use, so don't republish it.

## Development

```sh
gofmt -l . && go vet -tags sqlite_fts5 ./... && go test -tags sqlite_fts5 ./...
```
