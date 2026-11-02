// Command baatplassen keeps a searchable archive of the baatplassen.no forum and serves it
// to Claude.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nielth/baatplassen-mcp/internal/crawl"
	"github.com/nielth/baatplassen-mcp/internal/db"
	"github.com/nielth/baatplassen-mcp/internal/search"
	"github.com/nielth/baatplassen-mcp/internal/server"
)

const usage = `usage: baatplassen <command> [options]

commands:
  discover [--delay S]                  queue new and changed topics
  crawl [--delay S] [--max-topics N]    download queued topics
  run [--delay S]                       finish the crawl, then look for new posts every 24 hours
  status                                show crawl coverage
  search QUERY [--limit N]              search from the terminal
  words PATTERN                         list words the forum uses
  serve [--http]                        run the MCP server for Claude
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseArgs parses flags that may come before or after the positional arguments.
func parseArgs(flags *flag.FlagSet, args []string) ([]string, error) {
	positional := []string{}
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
}

// withLockedArchive runs one crawler job, holding the lock that keeps out any other crawler.
func withLockedArchive(job func(conn *sql.DB) error) error {
	lock, err := crawl.AcquireLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	conn, err := db.Connect()
	if err != nil {
		return err
	}
	defer conn.Close()
	return job(conn)
}

func withReadonlyArchive(job func(conn *sql.DB) (any, error)) error {
	conn, err := db.ConnectReadonly()
	if err != nil {
		return err
	}
	defer conn.Close()
	result, err := job(conn)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func run(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ExitOnError)
	delay := flags.Float64("delay", 1.0, "seconds between requests")
	maxTopics := flags.Int("max-topics", -1, "stop after this many topics")
	limit := flags.Int("limit", 10, "results to show")
	http := flags.Bool("http", false, "serve over HTTP")
	positional, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	needArgument := func() (string, error) {
		if len(positional) != 1 {
			return "", fmt.Errorf("%s takes one argument\n\n%s", command, usage)
		}
		return positional[0], nil
	}

	switch command {
	case "discover":
		return withLockedArchive(func(conn *sql.DB) error {
			return crawl.Discover(conn, seconds(*delay))
		})
	case "crawl":
		return withLockedArchive(func(conn *sql.DB) error {
			return crawl.Crawl(conn, seconds(*delay), *maxTopics)
		})
	case "run":
		return withLockedArchive(func(conn *sql.DB) error {
			return crawl.RunForever(conn, seconds(*delay))
		})
	case "status":
		return withReadonlyArchive(func(conn *sql.DB) (any, error) {
			return search.ArchiveStatus(conn)
		})
	case "search":
		query, err := needArgument()
		if err != nil {
			return err
		}
		return withReadonlyArchive(func(conn *sql.DB) (any, error) {
			options := search.DefaultOptions()
			options.Limit = *limit
			return search.SearchPosts(conn, query, options)
		})
	case "words":
		pattern, err := needArgument()
		if err != nil {
			return err
		}
		return withReadonlyArchive(func(conn *sql.DB) (any, error) {
			return search.FindWords(conn, pattern, 50)
		})
	case "serve":
		if *http {
			return server.ServeHTTP()
		}
		return server.Serve()
	}
	return fmt.Errorf("unknown command %q\n\n%s", command, usage)
}
