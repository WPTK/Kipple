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

// POST /api/reorder moves a folder and saves the new order in one request: a folders entry is an id
// (the folder keeps its parent) or {id, parent_id}. A move is refused with the folder writer's codes,
// and any refusal writes nothing.
func TestReorderMovesFolders(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	tech := h.addFolder("Tech")
	apple := h.addFolder("Apple")
	sports := h.addFolder("Sports")
	code, body, _ := h.api(c, "PATCH", "/api/folders/"+sid(apple), `{"parent_id":"`+sid(tech)+`"}`)
	require.Equal(t, 200, code, body)
	sub := h.events()

	// Apple out of Tech to the top level, between Tech and Sports.
	move := `{"folders":[1,"` + sid(tech) + `",{"id":"` + sid(apple) + `","parent_id":null},` + sid(sports) + `]}`
	code, body, _ = h.api(c, "POST", "/api/reorder", move)
	require.Equal(t, 200, code, body)
	require.Contains(t, body["changed_folders"], sid(apple))
	fp := positions(h, "folders")
	require.Equal(t, [2]int64{2, 0}, fp[apple])
	require.Equal(t, [2]int64{3, 0}, fp[sports])
	require.Equal(t, []string{`{}`}, folderChanged(t, sub))
	code, body, _ = h.api(c, "POST", "/api/reorder", move)
	require.Equal(t, 200, code)
	require.Empty(t, body["changed_folders"], "the same move again changes nothing")
	require.Empty(t, folderChanged(t, sub))

	// And back inside Tech, with numeric ids.
	code, body, _ = h.api(c, "POST", "/api/reorder", `{"folders":[1,`+sid(tech)+`,{"id":`+sid(apple)+`,"parent_id":`+sid(tech)+`},`+sid(sports)+`]}`)
	require.Equal(t, 200, code, body)
	require.Equal(t, [2]int64{2, tech}, positions(h, "folders")[apple])
	folderChanged(t, sub)

	topApple := h.addFolder("Apple") // the same name at the top level
	other := h.addFolder("Other")
	before := positions(h, "folders")
	obj := func(id int64, parent string) string { return `{"id":"` + sid(id) + `","parent_id":` + parent + `}` }
	for name, tc := range map[string]struct {
		entry string
		code  int
		err   string
	}{
		"cycle":           {obj(tech, `"`+sid(apple)+`"`), 409, "folder_cycle"},
		"default moved":   {obj(1, `"`+sid(tech)+`"`), 409, "default_folder"},
		"into default":    {obj(sports, `"1"`), 409, "default_folder"},
		"path taken":      {obj(topApple, `"`+sid(tech)+`"`), 409, "folder_exists"},
		"no parent":       {obj(sports, `"999"`), 400, "folder_not_found"},
		"no folder":       {obj(999, `null`), 400, "bad_request"},
		"parent_id 0":     {obj(sports, `0`), 400, "bad_request"},
		"parent_id bad":   {obj(sports, `"nope"`), 400, "bad_request"},
		"no parent_id":    {`{"id":"` + sid(sports) + `"}`, 400, "bad_request"},
		"no id":           {`{"parent_id":null}`, 400, "bad_request"},
		"extra key":       {`{"id":"` + sid(sports) + `","parent_id":null,"x":1}`, 400, "bad_request"},
		"list not object": {`[` + sid(sports) + `]`, 400, "bad_request"},
	} {
		// A good reposition before the refused entry and a feed list after it: nothing is written.
		body := `{"folders":[` + sid(other) + `,` + tc.entry + `],"feeds":[{"folder_id":` + sid(other) + `,"ids":[]}]}`
		code, resp, _ := h.api(c, "POST", "/api/reorder", body)
		require.Equal(t, tc.code, code, name)
		require.Equal(t, tc.err, resp["error"], name)
		require.Equal(t, before, positions(h, "folders"), name)
	}
	require.Empty(t, folderChanged(t, sub), "a refused reorder announces nothing")

	// Too deep: a chain of seven levels takes no two-level subtree.
	parent := int64(0)
	for i := 1; i <= 7; i++ {
		b := `{"name":"L` + sid(int64(i)) + `"}`
		if parent != 0 {
			b = `{"name":"L` + sid(int64(i)) + `","parent_id":"` + sid(parent) + `"}`
		}
		code, resp, _ := h.api(c, "POST", "/api/folders", b)
		require.Equal(t, 201, code, resp)
		fmt.Sscan(resp["id"].(string), &parent)
	}
	before = positions(h, "folders")
	code, body, _ = h.api(c, "POST", "/api/reorder", `{"folders":[`+obj(tech, `"`+sid(parent)+`"`)+`]}`)
	require.Equal(t, 409, code)
	require.Equal(t, "folder_too_deep", body["error"])
	require.Equal(t, before, positions(h, "folders"))
}
