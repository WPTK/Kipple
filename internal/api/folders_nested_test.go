package api

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// POST and PATCH /api/folders take parent_id (a folder id, or null for the top level); the writer's
// refusals are 409s with their own codes, and bootstrap lists parent_id.
func TestNestedFoldersAPI(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	code, body, _ := h.api(c, "POST", "/api/folders", `{"name":"Tech"}`)
	require.Equal(t, 201, code, body)
	require.Nil(t, body["parent_id"])
	tech := body["id"].(string)
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"Apple","parent_id":"`+tech+`"}`)
	require.Equal(t, 201, code, body)
	require.Equal(t, tech, body["parent_id"])
	apple := body["id"].(string)
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"apple","parent_id":"`+tech+`"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "folder_exists", body["error"])
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"Apple","parent_id":null}`)
	require.Equal(t, 201, code, "the same name at another level")
	topApple := body["id"].(string)
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"x","parent_id":"999"}`)
	require.Equal(t, 400, code)
	require.Equal(t, "folder_not_found", body["error"])
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"x","parent_id":"1"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "default_folder", body["error"])
	for _, b := range []string{`{"name":"x","parent_id":"nope"}`, `{"name":"x","parent_id":0}`, `{"name":"x","parent_id":"0"}`} {
		code, _, _ = h.api(c, "POST", "/api/folders", b)
		require.Equal(t, 400, code, b)
	}

	// Moves.
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+tech, `{"parent_id":"`+apple+`"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "folder_cycle", body["error"])
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+topApple, `{"parent_id":"`+tech+`"}`)
	require.Equal(t, 409, code, "Tech already holds an Apple")
	require.Equal(t, "folder_exists", body["error"])
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+apple, `{"parent_id":null,"name":"Apple2"}`)
	require.Equal(t, 200, code, body)
	require.Nil(t, body["parent_id"])
	require.Equal(t, "Apple2", body["name"])
	code, body, _ = h.api(c, "PATCH", "/api/folders/1", `{"parent_id":"`+tech+`"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "default_folder", body["error"])
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+apple, `{"parent_id":"999"}`)
	require.Equal(t, 400, code)
	require.Equal(t, "folder_not_found", body["error"])
	parent := tech
	for i := 2; i <= 8; i++ {
		code, body, _ = h.api(c, "POST", "/api/folders", fmt.Sprintf(`{"name":"L%d","parent_id":"%s"}`, i, parent))
		require.Equal(t, 201, code, body)
		parent = body["id"].(string)
	}
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"L9","parent_id":"`+parent+`"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "folder_too_deep", body["error"])

	// Bootstrap: parent_id per folder, and a parent's unread counts its subtree.
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+apple, `{"parent_id":"`+tech+`"}`)
	require.Equal(t, 200, code, body)
	var appleID int64
	fmt.Sscan(apple, &appleID)
	feed := h.addFeed("Deep", appleID)
	h.addItem(feed, seedItem{})
	code, body, _ = h.api(c, "GET", "/api/bootstrap", "")
	require.Equal(t, 200, code)
	byID := map[string]map[string]any{}
	for _, f := range body["folders"].([]any) {
		m := f.(map[string]any)
		byID[m["id"].(string)] = m
	}
	require.Equal(t, tech, byID[apple]["parent_id"])
	require.Nil(t, byID[tech]["parent_id"])
	require.EqualValues(t, 1, byID[apple]["unread"])
	require.EqualValues(t, 1, byID[tech]["unread"])

	// DELETE takes the subtree.
	code, _, _ = h.api(c, "DELETE", "/api/folders/"+tech, "")
	require.Equal(t, 204, code)
	require.Equal(t, 1, h.count("SELECT count(*) FROM feeds WHERE id = ? AND folder_id = 1", feed))
	require.Equal(t, 2, h.count("SELECT count(*) FROM folders"), "Uncategorized and the top-level Apple")
}
