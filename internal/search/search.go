// Package search has the queries behind both the MCP tools and the command line.
package search

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/nielth/baatplassen-mcp/internal/parse"
)

var (
	retryableQueryError = regexp.MustCompile(`fts5|no such column|syntax error`)
	wildcard            = regexp.MustCompile(`[*?\[]`)
)

// Hit is one search result.
type Hit struct {
	PostID          int64   `json:"post_id"`
	TopicID         int64   `json:"topic_id"`
	TopicTitle      *string `json:"topic_title"`
	Forum           *string `json:"forum"`
	Author          string  `json:"author"`
	PostedAt        *string `json:"posted_at"`
	PositionInTopic int     `json:"position_in_topic"`
	Snippet         *string `json:"snippet"`
	URL             string  `json:"url"`
}

// Word is a word the forum uses and how many posts use it.
type Word struct {
	Word  string `json:"word"`
	Posts int    `json:"posts"`
}

// TopicPost is one post of a topic, with the text it quotes.
type TopicPost struct {
	PostID   int64   `json:"post_id"`
	Author   string  `json:"author"`
	PostedAt *string `json:"posted_at"`
	Text     *string `json:"text"`
	URL      string  `json:"url"`
}

// Topic is a page of a topic's posts.
type Topic struct {
	TopicID     int64       `json:"topic_id"`
	Title       *string     `json:"title"`
	Forum       *string     `json:"forum"`
	URL         string      `json:"url"`
	CrawlStatus string      `json:"crawl_status"`
	TotalPosts  int         `json:"total_posts"`
	Offset      int         `json:"offset"`
	Posts       []TopicPost `json:"posts"`
}

// TopicError is what GetTopic returns for a topic that doesn't exist.
type TopicError struct {
	Error string `json:"error"`
}

// ForumCount is a forum and how many topics it has.
type ForumCount struct {
	Forum  string `json:"forum"`
	Topics int    `json:"topics"`
}

// Status reports crawl coverage and the forums available for filtering.
type Status struct {
	TopicsByStatus map[string]int `json:"topics_by_status"`
	Posts          int            `json:"posts"`
	OldestPost     *string        `json:"oldest_post"`
	NewestPost     *string        `json:"newest_post"`
	Incomplete     int            `json:"done_topics_with_fewer_posts_than_expected"`
	Forums         []ForumCount   `json:"forums"`
}

// Options narrows a search. Empty strings mean no filter.
type Options struct {
	Forum       string
	Author      string
	Since       string
	Until       string
	Limit       int
	Offset      int
	MaxPerTopic int
}

// DefaultOptions are the defaults for a search.
func DefaultOptions() Options {
	return Options{Limit: 20, MaxPerTopic: 3}
}

func postURL(topicURL string, postID int64) string {
	return fmt.Sprintf("%s?do=findComment&comment=%d", topicURL, postID)
}

// quoteTerms turns free text into valid FTS5 syntax, keeping a trailing * as a prefix search.
func quoteTerms(query string) string {
	terms := []string{}
	for _, term := range strings.FieldsFunc(query, parse.IsSpace) {
		prefix := strings.HasSuffix(term, "*")
		cleaned := strings.ReplaceAll(strings.TrimRight(term, "*"), `"`, "")
		if cleaned == "" {
			continue
		}
		quoted := `"` + cleaned + `"`
		if prefix {
			quoted += "*"
		}
		terms = append(terms, quoted)
	}
	return strings.Join(terms, " ")
}

type rankedRow struct {
	postID, topicID      int64
	author               string
	postedAt, title, frm *string
	url                  string
}

func runRanking(conn *sql.DB, query string, statement string, args []any) ([]rankedRow, error) {
	rows, err := conn.Query(statement, append(args, sql.Named("query", query))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []rankedRow{}
	for rows.Next() {
		var row rankedRow
		if err := rows.Scan(&row.postID, &row.topicID, &row.author, &row.postedAt, &row.title, &row.frm, &row.url); err != nil {
			return nil, err
		}
		found = append(found, row)
	}
	return found, rows.Err()
}

// SearchPosts ranks posts by relevance, with title matches weighted above body matches.
func SearchPosts(conn *sql.DB, query string, options Options) ([]Hit, error) {
	filters := []string{"posts_fts MATCH :query"}
	args := []any{
		sql.Named("limit", options.Limit),
		sql.Named("offset", options.Offset),
		sql.Named("max_per_topic", options.MaxPerTopic),
	}
	if options.Forum != "" {
		filters = append(filters, "t.forum LIKE :forum")
		args = append(args, sql.Named("forum", "%"+options.Forum+"%"))
	}
	if options.Author != "" {
		filters = append(filters, "p.author = :author COLLATE NOCASE")
		args = append(args, sql.Named("author", options.Author))
	}
	if options.Since != "" {
		filters = append(filters, "p.posted_at >= :since")
		args = append(args, sql.Named("since", options.Since))
	}
	if options.Until != "" {
		filters = append(filters, "p.posted_at < :until")
		args = append(args, sql.Named("until", options.Until))
	}

	statement := `
        WITH ranked AS (
            SELECT
                p.id AS post_id, p.topic_id, p.author, p.posted_at,
                t.title, t.forum, t.url,
                bm25(posts_fts, 5.0, 1.0, 0.0) AS score
            FROM posts_fts
            JOIN posts p ON p.id = posts_fts.rowid
            JOIN topics t ON t.id = p.topic_id
            WHERE ` + strings.Join(filters, " AND ") + `
        ),
        limited AS (
            SELECT *, ROW_NUMBER() OVER (PARTITION BY topic_id ORDER BY score) AS topic_rank
            FROM ranked
        )
        SELECT post_id, topic_id, author, posted_at, title, forum, url FROM limited
        WHERE topic_rank <= :max_per_topic
        ORDER BY score
        LIMIT :limit OFFSET :offset
    `
	rows, err := runRanking(conn, query, statement, args)
	if err != nil {
		// e.g. "volvo-penta" is invalid FTS5 syntax. Retry treating terms literally.
		if !retryableQueryError.MatchString(err.Error()) {
			return nil, err
		}
		query = quoteTerms(query)
		if rows, err = runRanking(conn, query, statement, args); err != nil {
			return nil, err
		}
	}

	results := []Hit{}
	for _, row := range rows {
		hit := Hit{
			PostID: row.postID, TopicID: row.topicID, TopicTitle: row.title, Forum: row.frm,
			Author: row.author, PostedAt: row.postedAt, URL: postURL(row.url, row.postID),
		}
		if err := conn.QueryRow(
			"SELECT COUNT(*) FROM posts WHERE topic_id = ? AND id < ?", row.topicID, row.postID,
		).Scan(&hit.PositionInTopic); err != nil {
			return nil, err
		}
		// Built per result: doing this in the ranking query makes common terms very slow.
		if err := conn.QueryRow(`
            SELECT snippet(posts_fts, 1, '«', '»', ' … ', 48) FROM posts_fts
            WHERE posts_fts MATCH ? AND rowid = ?
            `, query, row.postID).Scan(&hit.Snippet); err != nil {
			return nil, err
		}
		results = append(results, hit)
	}
	return results, nil
}

// FindWords lists words used on the forum that match a pattern, most widely used first. A
// pattern without wildcards matches anywhere in a word. This scans the whole word list, so
// it takes a while on a full archive.
func FindWords(conn *sql.DB, pattern string, limit int) ([]Word, error) {
	glob := strings.ToLower(pattern)
	if !wildcard.MatchString(glob) {
		glob = "*" + glob + "*"
	}
	// A temp table, because the archive is opened read-only.
	if _, err := conn.Exec(`
        CREATE VIRTUAL TABLE IF NOT EXISTS temp.posts_vocab
        USING fts5vocab(main, posts_fts, 'row')
        `); err != nil {
		return nil, err
	}
	rows, err := conn.Query(`
        SELECT term, doc FROM temp.posts_vocab
        WHERE term GLOB ? ORDER BY doc DESC, term LIMIT ?
        `, glob, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	words := []Word{}
	for rows.Next() {
		var word Word
		if err := rows.Scan(&word.Word, &word.Posts); err != nil {
			return nil, err
		}
		words = append(words, word)
	}
	return words, rows.Err()
}

// GetTopic returns a topic's posts in order, with quotes included: a Topic, or a TopicError
// if there is no such topic.
func GetTopic(conn *sql.DB, topicID int64, offset, limit int) (any, error) {
	topic := Topic{Offset: offset, Posts: []TopicPost{}}
	err := conn.QueryRow(
		"SELECT id, title, forum, url, status FROM topics WHERE id = ?", topicID,
	).Scan(&topic.TopicID, &topic.Title, &topic.Forum, &topic.URL, &topic.CrawlStatus)
	if err == sql.ErrNoRows {
		return TopicError{Error: fmt.Sprintf("No topic with id %d", topicID)}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := conn.QueryRow(
		"SELECT COUNT(*) FROM posts WHERE topic_id = ?", topicID,
	).Scan(&topic.TotalPosts); err != nil {
		return nil, err
	}
	rows, err := conn.Query(`
        SELECT p.id, p.author, p.posted_at, COALESCE(f.full_text, f.body) AS text
        FROM posts p JOIN posts_fts f ON f.rowid = p.id
        WHERE p.topic_id = ?
        ORDER BY p.id
        LIMIT ? OFFSET ?
        `, topicID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var post TopicPost
		if err := rows.Scan(&post.PostID, &post.Author, &post.PostedAt, &post.Text); err != nil {
			return nil, err
		}
		post.URL = postURL(topic.URL, post.PostID)
		topic.Posts = append(topic.Posts, post)
	}
	return topic, rows.Err()
}

// ArchiveStatus reports crawl coverage and the forums available for filtering.
func ArchiveStatus(conn *sql.DB) (*Status, error) {
	status := Status{TopicsByStatus: map[string]int{}, Forums: []ForumCount{}}
	rows, err := conn.Query("SELECT status, COUNT(*) AS n FROM topics GROUP BY status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		status.TopicsByStatus[name] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	if err := conn.QueryRow(
		"SELECT COUNT(*) AS n, MIN(posted_at) AS oldest, MAX(posted_at) AS newest FROM posts",
	).Scan(&status.Posts, &status.OldestPost, &status.NewestPost); err != nil {
		return nil, err
	}
	if err := conn.QueryRow(`
        SELECT COUNT(*) FROM (
            SELECT t.id FROM topics t JOIN posts p ON p.topic_id = t.id
            WHERE t.status = 'done' AND t.expected_posts IS NOT NULL
            GROUP BY t.id HAVING COUNT(*) < t.expected_posts
        )
        `).Scan(&status.Incomplete); err != nil {
		return nil, err
	}

	forums, err := conn.Query(`
        SELECT forum, COUNT(*) AS topics FROM topics
        WHERE forum IS NOT NULL GROUP BY forum ORDER BY topics DESC
        `)
	if err != nil {
		return nil, err
	}
	defer forums.Close()
	for forums.Next() {
		var forum ForumCount
		if err := forums.Scan(&forum.Forum, &forum.Topics); err != nil {
			return nil, err
		}
		status.Forums = append(status.Forums, forum)
	}
	return &status, forums.Err()
}
