package search

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nielth/baatplassen-mcp/internal/db"
)

func archive(t *testing.T) *sql.DB {
	t.Helper()
	db.Path = filepath.Join(t.TempDir(), "archive.db")
	writer, err := db.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	statements := []string{
		`INSERT INTO topics (id, url, title, forum, status, expected_posts) VALUES
            (1, 'https://f/topic/1-a/', 'Motor', 'Båtforumet', 'done', 3),
            (2, 'https://f/topic/2-b/', 'Seil', 'Seiling', 'pending', NULL)`,
		`INSERT INTO posts (id, topic_id, page, author, posted_at) VALUES
            (10, 1, 1, 'Ola', '2020-01-01T00:00:00Z'),
            (11, 1, 1, 'Kari', '2021-01-01T00:00:00Z'),
            (12, 2, 1, 'Ola', '2022-01-01T00:00:00Z')`,
		`INSERT INTO posts_fts (rowid, title, body, full_text) VALUES
            (10, 'Motor', 'Dieselmotor og påhengsmotor på volvo-penta', NULL),
            (11, 'Motor', 'Ny påhengsmotor', '> Sitat: x
Ny påhengsmotor'),
            (12, 'Seil', 'Seil til båten', NULL)`,
	}
	for _, statement := range statements {
		if _, err := writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	readonly, err := db.ConnectReadonly()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { readonly.Close() })
	return readonly
}

func TestFindWordsOnAReadOnlyDatabase(t *testing.T) {
	conn := archive(t)
	got, err := FindWords(conn, "*motor", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []Word{{"motor", 2}, {"påhengsmotor", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	got, err = FindWords(conn, "PÅHENGS", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Word{{"påhengsmotor", 2}}) {
		t.Errorf("got %v", got)
	}
	got, err = FindWords(conn, "s?il", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Word{{"seil", 1}}) {
		t.Errorf("got %v", got)
	}
}

func hitIDs(t *testing.T, conn *sql.DB, query string, options Options) []int64 {
	t.Helper()
	hits, err := SearchPosts(conn, query, options)
	if err != nil {
		t.Fatalf("%q: %v", query, err)
	}
	ids := []int64{}
	for _, hit := range hits {
		ids = append(ids, hit.PostID)
	}
	return ids
}

func TestSearchPosts(t *testing.T) {
	conn := archive(t)
	base := DefaultOptions()
	cases := []struct {
		name    string
		query   string
		options func(*Options)
		want    []int64
	}{
		{"word", "båten", func(*Options) {}, []int64{12}},
		{"invalid syntax falls back to literal terms", "volvo-penta", func(*Options) {}, []int64{10}},
		{"literal fallback keeps prefix", "volvo-pen*", func(*Options) {}, []int64{10}},
		{"author is case-insensitive", "seil", func(o *Options) { o.Author = "OLA" }, []int64{12}},
		{"forum matches part of the name", "seil", func(o *Options) { o.Forum = "Seil" }, []int64{12}},
		{"forum excludes", "seil", func(o *Options) { o.Forum = "Båt" }, []int64{}},
		{"since", "påhengsmotor", func(o *Options) { o.Since = "2021-01-01" }, []int64{11}},
		{"until", "påhengsmotor", func(o *Options) { o.Until = "2021-01-01" }, []int64{10}},
		{"one per topic", "påhengsmotor", func(o *Options) { o.MaxPerTopic = 1; o.Limit = 5 }, nil},
		{"no results", "finnesikke", func(*Options) {}, []int64{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			options := base
			c.options(&options)
			got := hitIDs(t, conn, c.query, options)
			if c.want == nil {
				if len(got) != 1 {
					t.Errorf("got %v, want one result", got)
				}
			} else if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestSearchHitDetails(t *testing.T) {
	hits, err := SearchPosts(archive(t), "ny", DefaultOptions())
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %v, err = %v", hits, err)
	}
	hit := hits[0]
	if hit.PositionInTopic != 1 || *hit.Snippet != "«Ny» påhengsmotor" {
		t.Errorf("position = %d, snippet = %q", hit.PositionInTopic, *hit.Snippet)
	}
	if hit.URL != "https://f/topic/1-a/?do=findComment&comment=11" {
		t.Errorf("url = %s", hit.URL)
	}
}

func TestUnusableQueryIsAnError(t *testing.T) {
	conn := archive(t)
	for _, query := range []string{`"unterminated`, "***"} {
		if _, err := SearchPosts(conn, query, DefaultOptions()); err == nil {
			t.Errorf("%q: no error", query)
		}
	}
}

func TestGetTopic(t *testing.T) {
	conn := archive(t)
	result, err := GetTopic(conn, 1, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	topic := result.(Topic)
	if topic.TotalPosts != 2 || topic.Offset != 1 || len(topic.Posts) != 1 {
		t.Fatalf("topic = %+v", topic)
	}
	if *topic.Posts[0].Text != "> Sitat: x\nNy påhengsmotor" {
		t.Errorf("text should include the quote: %q", *topic.Posts[0].Text)
	}

	missing, err := GetTopic(conn, 99, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if missing != (TopicError{Error: "No topic with id 99"}) {
		t.Errorf("missing = %+v", missing)
	}
}

func TestArchiveStatus(t *testing.T) {
	status, err := ArchiveStatus(archive(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status.TopicsByStatus, map[string]int{"done": 1, "pending": 1}) {
		t.Errorf("statuses = %v", status.TopicsByStatus)
	}
	if status.Posts != 3 || *status.OldestPost != "2020-01-01T00:00:00Z" || *status.NewestPost != "2022-01-01T00:00:00Z" {
		t.Errorf("status = %+v", status)
	}
	if status.Incomplete != 1 {
		t.Errorf("incomplete = %d", status.Incomplete)
	}
	if len(status.Forums) != 2 {
		t.Errorf("forums = %v", status.Forums)
	}
}
