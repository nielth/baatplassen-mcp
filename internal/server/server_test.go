package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nielth/baatplassen-mcp/internal/db"
)

const secret = "this-is-a-test-secret-of-enough-length"

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	db.Path = filepath.Join(t.TempDir(), "archive.db")
	writer, err := db.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for _, statement := range []string{
		`INSERT INTO topics (id, url, title, forum) VALUES (1, 'https://f/topic/1-a/', 'Motor', 'Båtforumet')`,
		`INSERT INTO posts (id, topic_id, page, author) VALUES (10, 1, 1, 'Ola'), (11, 1, 1, 'Kari')`,
		`INSERT INTO posts_fts (rowid, title, body, full_text) VALUES
            (10, 'Motor', 'Impeller <og> kjøling [bilde: https://example.com/a.jpg]', NULL),
            (11, 'Motor', 'Svar', '> Ola skrev:
> Impeller

Svar')`,
	} {
		if _, err := writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(Handler(secret))
	t.Cleanup(server.Close)
	return server
}

func call(t *testing.T, server *httptest.Server, method string, params any) map[string]any {
	t.Helper()
	return callAs(t, server, secret, method, params)
}

// callAs makes an MCP request on another user's secret.
func callAs(t *testing.T, server *httptest.Server, userSecret, method string, params any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/"+userSecret+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", response.StatusCode, data)
	}
	var decoded struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return decoded.Result
}

func callTool(t *testing.T, server *httptest.Server, name string, arguments map[string]any) map[string]any {
	t.Helper()
	return call(t, server, "tools/call", map[string]any{"name": name, "arguments": arguments})
}

func texts(result map[string]any) []string {
	found := []string{}
	content, _ := result["content"].([]any)
	for _, item := range content {
		block, _ := item.(map[string]any)
		if text, ok := block["text"].(string); ok {
			found = append(found, text)
		}
	}
	return found
}

func TestOnlyTheSecretPathAnswers(t *testing.T) {
	server := testServer(t)
	for _, path := range []string{
		"/", "/mcp", "/" + secret, "/wrong/mcp", "/" + secret + "/mcp/", "/" + secret + "x/mcp",
		"/notes", "/wrong/notes", "/" + secret[:len(secret)-1] + "/notes",
	} {
		response, err := http.Post(server.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d", path, response.StatusCode)
		}
	}
}

func TestListsTheTools(t *testing.T) {
	result := call(t, testServer(t), "tools/list", map[string]any{})
	tools := result["tools"].([]any)
	names := map[string]bool{}
	for _, item := range tools {
		tool := item.(map[string]any)
		names[tool["name"].(string)] = true
		hints, _ := tool["annotations"].(map[string]any)
		if destructive := hints["destructiveHint"] == true; destructive != (tool["name"] == "delete_note") {
			t.Errorf("%s: destructive = %v", tool["name"], destructive)
		}
	}
	for _, name := range []string{
		"search_posts", "get_topic", "archive_status", "find_words", "get_post_images",
		"save_note", "search_notes", "delete_note", "get_note_images",
	} {
		if !names[name] {
			t.Errorf("missing %s", name)
		}
	}
}

func TestSearchReturnsOneBlockPerResultAndStructuredContent(t *testing.T) {
	result := callTool(t, testServer(t), "search_posts", map[string]any{"query": "impeller", "forum": nil})
	if result["isError"] == true {
		t.Fatalf("error: %v", result)
	}
	blocks := texts(result)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %v", blocks)
	}
	if !strings.Contains(blocks[0], `"post_id": 10`) {
		t.Errorf("not indented JSON: %s", blocks[0])
	}
	if !strings.Contains(blocks[0], "<og>") {
		t.Errorf("HTML characters escaped: %s", blocks[0])
	}
	structured := result["structuredContent"].(map[string]any)["result"].([]any)
	if len(structured) != 1 {
		t.Errorf("structured = %v", structured)
	}
}

func TestEmptySearchIsAnEmptyListNotNull(t *testing.T) {
	result := callTool(t, testServer(t), "search_posts", map[string]any{"query": "finnesikke"})
	if len(texts(result)) != 0 {
		t.Errorf("blocks = %v", texts(result))
	}
	list, ok := result["structuredContent"].(map[string]any)["result"].([]any)
	if !ok || len(list) != 0 {
		t.Errorf("structured = %v", result["structuredContent"])
	}
}

func TestLimitsAreCapped(t *testing.T) {
	server := testServer(t)
	result := callTool(t, server, "find_words", map[string]any{"pattern": "*", "limit": 1})
	if len(texts(result)) != 1 {
		t.Errorf("limit ignored: %v", texts(result))
	}
}

func TestGetTopicKeepsQuoteMarkers(t *testing.T) {
	result := callTool(t, testServer(t), "get_topic", map[string]any{"topic_id": 1, "offset": 1})
	blocks := texts(result)
	if len(blocks) != 1 || !strings.Contains(blocks[0], `"text": "> Ola skrev:`) {
		t.Errorf("blocks = %v", blocks)
	}
	if result["structuredContent"].(map[string]any)["total_posts"] != float64(2) {
		t.Errorf("structured = %v", result["structuredContent"])
	}
}

func TestMissingTopicAndPostAreNotErrors(t *testing.T) {
	server := testServer(t)
	result := callTool(t, server, "get_topic", map[string]any{"topic_id": 99})
	if result["isError"] == true || result["structuredContent"].(map[string]any)["error"] != "No topic with id 99" {
		t.Errorf("result = %v", result)
	}
	result = callTool(t, server, "get_post_images", map[string]any{"post_id": 99})
	if result["isError"] == true || texts(result)[0] != "No post with id 99" {
		t.Errorf("result = %v", result)
	}
}

func TestImageOnAnotherSiteComesBackAsAReason(t *testing.T) {
	result := callTool(t, testServer(t), "get_post_images", map[string]any{"post_id": 10})
	want := []string{
		"Post 10 has 1 image(s), showing 1.",
		"Image 0 (https://example.com/a.jpg) unavailable: hosted on another site, so it is not fetched automatically",
	}
	got := texts(result)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v", got)
	}
}

func TestImageStartPastTheEndAndNegativeAreSafe(t *testing.T) {
	server := testServer(t)
	for _, start := range []int{5, -3} {
		result := callTool(t, server, "get_post_images", map[string]any{"post_id": 10, "start": start})
		if result["isError"] == true {
			t.Errorf("start %d: %v", start, result)
		}
	}
}

func TestErrorsDoNotLeakDetails(t *testing.T) {
	result := callTool(t, testServer(t), "search_posts", map[string]any{"query": `"unterminated`})
	if result["isError"] != true || texts(result)[0] != "Error executing tool search_posts" {
		t.Errorf("result = %v", result)
	}
}

func TestMissingArgumentIsAToolError(t *testing.T) {
	result := callTool(t, testServer(t), "search_posts", map[string]any{})
	if result["isError"] != true || !strings.Contains(texts(result)[0], "query") {
		t.Errorf("result = %v", result)
	}
}
