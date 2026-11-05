package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

var newSecret = regexp.MustCompile(`value="https?://[^/"]+/([0-9a-f]{64})/mcp"`)

// createUserAs posts the form on the admin's page, and returns the page and the new secret.
func createUserAs(t *testing.T, server *httptest.Server, userSecret, name string) (*http.Response, string, string) {
	t.Helper()
	response, err := noRedirects.PostForm(server.URL+"/"+userSecret+"/users", url.Values{"name": {name}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	match := newSecret.FindStringSubmatch(body)
	if match == nil {
		return response, body, ""
	}
	return response, body, match[1]
}

func TestAdminGivesSomeoneTheirOwnAddress(t *testing.T) {
	server := testServer(t)
	response, page, kari := createUserAs(t, server, secret, "Kari")
	if response.StatusCode != http.StatusOK || kari == "" {
		t.Fatalf("status %d, page %s", response.StatusCode, page)
	}
	if !strings.Contains(page, "/"+kari+"/notes") || !strings.Contains(page, "Kari") {
		t.Errorf("page = %s", page)
	}
	// The new address works for MCP, including the shared forum archive.
	result := callAs(t, server, kari, "tools/call", map[string]any{"name": "search_posts", "arguments": map[string]any{"query": "impeller"}})
	if len(texts(result)) != 1 {
		t.Errorf("archive search = %v", result)
	}
	// And the secret is never shown again.
	if _, page := get(t, server, "/"+secret+"/notes"); strings.Contains(page, kari) || !strings.Contains(page, "Kari") {
		t.Errorf("page = %s", page)
	}
}

func TestUsersDoNotSeeEachOthersNotes(t *testing.T) {
	server := testServer(t)
	_, _, kari := createUserAs(t, server, secret, "Kari")
	callTool(t, server, "save_note", map[string]any{"text": "Admin sin motor"})
	postNote(t, server, "Admin sitt bilde", photo(t))
	result := callAs(t, server, kari, "tools/call", map[string]any{"name": "save_note", "arguments": map[string]any{"text": "Kari sin motor"}})
	if result["isError"] == true {
		t.Fatalf("result = %v", result)
	}
	search := func(userSecret string) []string {
		return texts(callAs(t, server, userSecret, "tools/call", map[string]any{"name": "search_notes", "arguments": map[string]any{"query": "motor"}}))
	}
	if got := search(kari); len(got) != 1 || !strings.Contains(got[0], "Kari sin motor") {
		t.Errorf("kari sees %v", got)
	}
	if got := search(secret); len(got) != 1 || !strings.Contains(got[0], "Admin sin motor") {
		t.Errorf("admin sees %v", got)
	}
	// Note 2 is the admin's photo note: Kari can't see, view or delete it.
	for _, name := range []string{"get_note_images", "delete_note"} {
		got := texts(callAs(t, server, kari, "tools/call", map[string]any{"name": name, "arguments": map[string]any{"note_id": 2}}))
		if got[0] != "No note with id 2" {
			t.Errorf("%s: %v", name, got)
		}
	}
	if response, _ := get(t, server, "/"+kari+"/notes/images/1"); response.StatusCode != http.StatusNotFound {
		t.Errorf("kari got the admin's photo: %d", response.StatusCode)
	}
	if _, page := get(t, server, "/"+kari+"/notes"); strings.Contains(page, "Admin") || !strings.Contains(page, "Kari sin motor") {
		t.Errorf("kari's page = %s", page)
	}
}

func TestOnlyTheAdminManagesAccess(t *testing.T) {
	server := testServer(t)
	_, _, kari := createUserAs(t, server, secret, "Kari")
	if response, _, other := createUserAs(t, server, kari, "Ola"); response.StatusCode != http.StatusNotFound || other != "" {
		t.Errorf("kari created a user: %d", response.StatusCode)
	}
	if _, page := get(t, server, "/"+kari+"/notes"); strings.Contains(page, "Tilgang for andre") {
		t.Errorf("kari sees the access section")
	}
	response, err := noRedirects.PostForm(server.URL+"/"+kari+"/users/revoke", url.Values{"id": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("kari revoked: %d", response.StatusCode)
	}
}

func TestRevokedAddressStopsWorking(t *testing.T) {
	server := testServer(t)
	_, _, kari := createUserAs(t, server, secret, "Kari")
	response, err := noRedirects.PostForm(server.URL+"/"+secret+"/users/revoke", url.Values{"id": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "../notes" {
		t.Errorf("status %d, location %q", response.StatusCode, response.Header.Get("Location"))
	}
	for _, path := range []string{"/" + kari + "/notes", "/" + kari + "/mcp"} {
		if response, _ := get(t, server, path); response.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d", path, response.StatusCode)
		}
	}
	if _, page := get(t, server, "/"+secret+"/notes"); strings.Contains(page, "Fjern tilgang") {
		t.Errorf("revoked user still listed")
	}
}

func TestNameIsRequired(t *testing.T) {
	response, page, created := createUserAs(t, testServer(t), secret, "   ")
	if response.StatusCode != http.StatusBadRequest || created != "" || !strings.Contains(page, "Navnet må") {
		t.Errorf("status %d, page %s", response.StatusCode, page)
	}
}

func TestNotesToolsFailWithoutAUser(t *testing.T) {
	result, err := saveNote(t.Context(), mcpRequest(map[string]any{"text": "x"}))
	if err != nil || result.IsError != true {
		t.Errorf("result = %v %v", result, err)
	}
}

func mcpRequest(arguments map[string]any) mcp.CallToolRequest {
	var request mcp.CallToolRequest
	request.Params.Arguments = arguments
	return request
}
