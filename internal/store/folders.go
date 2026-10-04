package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// Folders form a tree (migration 0012): folders.parent_id is NULL at the top level, and the view
// folder_paths gives every folder its full path ("Tech/Apple": the names joined with '/'), its depth
// and its place in display order. This file is the only writer of the folders table. The database
// enforces unique names among siblings, a top-level default folder and the cascade of a delete to the
// subfolders; the writer adds the rules a CHECK cannot express:
//
//   - full paths are unique, ignoring ASCII case, across levels: a top-level folder literally named
//     "AC/DC" and a folder "DC" inside "AC" would be one Reader API label, so the second is refused;
//   - a folder never moves inside itself or one of its subfolders;
//   - folders nest at most MaxFolderDepth levels;
//   - the default folder stays at the top level and holds no subfolders.
//
// Reader API clients see the tree as flat labels named by the full path (docs/design.md, "Folders and
// the Reader API"); the web app sees parent_id.

// MaxFolderDepth is how many levels folders nest, the top level included. It bounds the subtree a
// delete cascades through, the length of a Reader API label and the sidebar's indentation.
const MaxFolderDepth = 8

// MaxFolderNameRunes is the longest folder name, in characters, on every path that names a folder
// (the web UI, the Reader API, OPML import). It applies to each folder's own name, not to its path.
const MaxFolderNameRunes = 100

var (
	// ErrBadFolderName is returned for a folder name that is empty, longer than MaxFolderNameRunes or
	// holds a control character.
	ErrBadFolderName = errors.New("store: folder names are 1 to 100 characters without control characters")
	// ErrFolderDepth refuses a folder that would sit deeper than MaxFolderDepth levels.
	ErrFolderDepth = errors.New("store: folders nest at most 8 levels deep")
	// ErrFolderCycle refuses moving a folder inside itself or one of its subfolders.
	ErrFolderCycle = errors.New("store: a folder cannot move inside itself or one of its subfolders")
	// ErrFolderParent refuses a subfolder of the default folder, or moving the default folder.
	ErrFolderParent = errors.New("store: the default folder stays at the top level and holds no subfolders")
	// ErrMergeSubfolders refuses a Reader API rename onto an existing folder (a merge) when the renamed
	// folder has subfolders: two trees would have to be merged, which a flat client cannot even see it asked for.
	ErrMergeSubfolders = errors.New("store: a folder that has subfolders cannot be merged into another folder")
	// ErrEmptyFolderSegment refuses a folder path with an empty level: a leading, trailing or doubled '/'
	// ("News/", "/News", "A//B") or a level of spaces only.
	ErrEmptyFolderSegment = errors.New("store: a folder path has an empty level (a leading, trailing or doubled '/')")
	// ErrParentNotFound is a create or move into a parent folder that does not exist.
	ErrParentNotFound = errors.New("store: no such parent folder")
)

// FolderRefused reports whether err is a folder change the store refused because of the request
// itself (a bad name, a taken path, the tree rules, a merge it cannot do), never a database failure.
// The Reader API logs these and answers OK with nothing changed.
func FolderRefused(err error) bool {
	for _, e := range []error{ErrBadFolderName, ErrFolderExists, ErrFolderDepth, ErrFolderCycle, ErrFolderParent,
		ErrMergeSubfolders, ErrMergeTooManyFilters, ErrEmptyFolderSegment} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// CheckFolderName reports whether a (trimmed, non-empty) folder name may be stored: at most
// MaxFolderNameRunes characters and no control character (below 0x20 except tab, or DEL), the web
// UI's rule. A '/' is allowed: it is part of the name, and of the path's text.
func CheckFolderName(name string) error {
	if utf8.RuneCountInString(name) > MaxFolderNameRunes {
		return ErrBadFolderName
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return ErrBadFolderName
		}
	}
	return nil
}

// folderTreeSQL selects the id bound to param and the ids of all the folders below it.
func folderTreeSQL(param string) string {
	return "WITH RECURSIVE folder_tree(id) AS (SELECT " + param +
		" UNION ALL SELECT c.id FROM folders c JOIN folder_tree ON c.parent_id = folder_tree.id) SELECT id FROM folder_tree"
}

// folderChain returns a folder's id followed by its ancestors' ids, nearest first: what a folder-scoped
// filter matches against (a rule on a folder covers its subfolders' feeds).
func folderChain(ctx context.Context, q Querier, id int64) ([]int64, error) {
	return queryIDs(ctx, q, `WITH RECURSIVE up(id, parent_id, n) AS (
			SELECT id, parent_id, 0 FROM folders WHERE id = ?
			UNION ALL SELECT f.id, f.parent_id, up.n + 1 FROM folders f JOIN up ON f.id = up.parent_id)
		SELECT id FROM up ORDER BY n`, id)
}

// FindLabel resolves a folder by its full path (ignoring ASCII case), trying each candidate in order
// (design §6.2 label lookup).
func FindLabel(ctx context.Context, q Querier, candidates []string) (id int64, found bool, err error) {
	for _, c := range candidates {
		if strings.TrimSpace(c) == "" {
			continue
		}
		if id, found, err = folderByPath(ctx, q, c); err != nil || found {
			return id, found, err
		}
	}
	return 0, false, nil
}

// FindLabel is FindLabel on the reader pool.
func (d *DB) FindLabel(ctx context.Context, candidates []string) (int64, bool, error) {
	return FindLabel(ctx, d.reader, candidates)
}

// folderByPath finds the folder whose full path is path, ignoring ASCII case. It walks down from the
// top level on idx_folders_sibling_name instead of reading folder_paths (whose window function and
// recursion read every folder on each query): at each level it tries every run of the remaining
// '/'-separated parts as one name, so a name that holds a '/' (a literal "AC/DC") is found too. Full
// paths are unique, so the first complete match is the folder. A run longer than a folder name can be
// is not tried, and a (parent, part) that led nowhere is not tried twice.
func folderByPath(ctx context.Context, q Querier, path string) (int64, bool, error) {
	if path == "" {
		return 0, false, nil
	}
	parts := strings.Split(path, "/")
	type at struct {
		parent int64
		i      int
	}
	deadEnd := map[at]bool{}
	var find func(parent int64, i int) (int64, bool, error)
	find = func(parent int64, i int) (int64, bool, error) {
		if deadEnd[at{parent, i}] {
			return 0, false, nil
		}
		name := parts[i]
		for j := i; j < len(parts); j++ {
			if j > i {
				name += "/" + parts[j]
			}
			if utf8.RuneCountInString(name) > MaxFolderNameRunes {
				break
			}
			id, ok, err := childFolder(ctx, q, parent, name)
			if err != nil {
				return 0, false, err
			}
			if !ok {
				continue
			}
			if j == len(parts)-1 {
				return id, true, nil
			}
			if id, ok, err := find(id, j+1); err != nil || ok {
				return id, ok, err
			}
		}
		deadEnd[at{parent, i}] = true
		return 0, false, nil
	}
	return find(0, 0)
}

// FolderExists reports whether a folder id exists.
func (d *DB) FolderExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM folders WHERE id = ?", id).Scan(&n)
	return n > 0, err
}

// folderRow is what the writer needs to know about one folder.
type folderRow struct {
	parent    int64 // 0 at the top level
	name      string
	path      string
	depth     int
	isDefault bool
}

// loadFolder reads a folder and its ancestors by primary key (at most MaxFolderDepth rows), not
// through folder_paths, which reads every folder.
func loadFolder(ctx context.Context, q Querier, id int64) (folderRow, error) {
	var r folderRow
	names, err := folderNames(ctx, q, id, &r)
	if err != nil {
		return r, err
	}
	r.name, r.path, r.depth = names[len(names)-1], strings.Join(names, "/"), len(names)
	return r, nil
}

// folderNames returns the names from the top level down to folder id, and fills r's parent and
// isDefault when r is not nil. A missing folder is ErrFolderNotFound.
func folderNames(ctx context.Context, q Querier, id int64, r *folderRow) ([]string, error) {
	// One primary-key lookup per level (a recursive CTE here is planned as a scan of folders per level).
	var names []string
	for cur, n := id, 0; ; n++ {
		var parent sql.NullInt64
		var name string
		var isDefault bool
		err := q.QueryRowContext(ctx, "SELECT parent_id, name, is_default FROM folders WHERE id = ?", cur).Scan(&parent, &name, &isDefault)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrFolderNotFound
		}
		if err != nil {
			return nil, err
		}
		if n == 0 && r != nil {
			r.parent, r.isDefault = parent.Int64, isDefault
		}
		names = append(names, name)
		if !parent.Valid {
			break
		}
		if n >= 64 { // the writer keeps the tree acyclic and at most MaxFolderDepth deep
			return nil, errors.New("store: folder ancestry too deep")
		}
		cur = parent.Int64
	}
	slices.Reverse(names)
	return names, nil
}

// FolderNames returns a folder's chain of names from the top level (what an OPML file nests).
func FolderNames(ctx context.Context, q Querier, id int64) ([]string, error) {
	return folderNames(ctx, q, id, nil)
}

// placeFolder checks that folder id (0 for a new folder) may be called name and sit directly inside
// parent (0 = the top level), with all of its subfolders following it. The name is kept as written
// (callers trim what they take from a person; a Reader API label keeps its inner spaces). It returns name.
func placeFolder(ctx context.Context, tx *sql.Tx, id, parent int64, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", ErrBadFolderName
	}
	if err := CheckFolderName(name); err != nil {
		return "", err
	}
	path, depth := name, 1
	if parent != 0 {
		p, err := loadFolder(ctx, tx, parent)
		if errors.Is(err, ErrFolderNotFound) {
			return "", ErrParentNotFound
		}
		if err != nil {
			return "", err
		}
		if p.isDefault {
			return "", ErrFolderParent
		}
		path, depth = p.path+"/"+name, p.depth+1
	}
	if id == 0 {
		if depth > MaxFolderDepth {
			return "", ErrFolderDepth
		}
		_, taken, err := folderByPath(ctx, tx, path)
		if err != nil {
			return "", err
		}
		if taken {
			return "", ErrFolderExists
		}
		return name, nil
	}
	cur, err := loadFolder(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if cur.isDefault && parent != 0 {
		return "", ErrFolderParent
	}
	if parent != 0 {
		var inside int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM ("+folderTreeSQL("?1")+") WHERE id = ?2", id, parent).Scan(&inside); err != nil {
			return "", err
		}
		if inside > 0 {
			return "", ErrFolderCycle
		}
	}
	// Every folder of the subtree keeps its path below the moved or renamed folder: its new depth and
	// its new path (the new prefix plus what follows the old one) must both be free.
	var height, clashes int
	if err := tx.QueryRowContext(ctx, `WITH s(id, np, depth) AS (
			SELECT p.id, ?1 || substr(p.path, length(?2) + 1), p.depth FROM folder_paths p WHERE p.id IN (`+folderTreeSQL("?3")+`))
		SELECT (SELECT max(depth) FROM s) - ?4 + 1,
		       (SELECT count(*) FROM folder_paths o JOIN s ON o.path = s.np COLLATE NOCASE WHERE o.id NOT IN (SELECT id FROM s))`,
		path, cur.path, id, cur.depth).Scan(&height, &clashes); err != nil {
		return "", err
	}
	if depth+height-1 > MaxFolderDepth {
		return "", ErrFolderDepth
	}
	if clashes > 0 {
		return "", ErrFolderExists
	}
	return name, nil
}

// createFolder inserts a folder named name inside parent (0 = the top level) after placeFolder's
// checks. position < 0 puts it after every other folder.
func createFolder(ctx context.Context, tx *sql.Tx, parent int64, name string, position int64) (int64, error) {
	name, err := placeFolder(ctx, tx, 0, parent, name)
	if err != nil {
		return 0, err
	}
	var p any
	if parent != 0 {
		p = parent
	}
	var res sql.Result
	if position < 0 {
		res, err = tx.ExecContext(ctx, "INSERT INTO folders (parent_id, name, position) SELECT ?, ?, COALESCE(MAX(position)+1, 1) FROM folders", p, name)
	} else {
		res, err = tx.ExecContext(ctx, "INSERT INTO folders (parent_id, name, position) VALUES (?, ?, ?)", p, name, position)
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// placeAndSave moves folder id inside parent (0 = the top level) and renames it, after placeFolder's checks.
func placeAndSave(ctx context.Context, tx *sql.Tx, id, parent int64, name string) error {
	name, err := placeFolder(ctx, tx, id, parent, name)
	if err != nil {
		return err
	}
	var p any
	if parent != 0 {
		p = parent
	}
	_, err = tx.ExecContext(ctx, "UPDATE folders SET parent_id = ?, name = ? WHERE id = ? AND (parent_id IS NOT ? OR name COLLATE BINARY IS NOT ?)", p, name, id, p, name)
	return err
}

// childFolder finds the folder named name (ignoring ASCII case) directly inside parent (0 = the top level).
func childFolder(ctx context.Context, q Querier, parent int64, name string) (int64, bool, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM folders WHERE ifnull(parent_id, 0) = ? AND name = ? COLLATE NOCASE", parent, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// walkFolders follows segments down from parent (0 = the top level), each one the name of the next
// folder as written, and creates the ones that are missing. A path is never stored twice: when the full
// path of what is left (a top-level "AC/DC" for the segments "AC", "DC") or of the next level already
// belongs to a folder elsewhere in the tree, the walk resolves to that folder instead of creating one, so
// it never leaves an empty "AC" behind. An empty or all-space segment is ErrEmptyFolderSegment, before
// anything is created. It returns the last folder and how many it created.
func walkFolders(ctx context.Context, tx *sql.Tx, parent int64, segments []string) (id int64, created int, err error) {
	for _, seg := range segments {
		if strings.TrimSpace(seg) == "" {
			return 0, 0, ErrEmptyFolderSegment
		}
		if err := CheckFolderName(seg); err != nil {
			return 0, 0, err
		}
	}
	path := ""
	if parent != 0 {
		p, err := loadFolder(ctx, tx, parent)
		if err != nil {
			return 0, 0, err
		}
		path = p.path
	}
	join := func(base string, segs ...string) string {
		if base == "" {
			return strings.Join(segs, "/")
		}
		return base + "/" + strings.Join(segs, "/")
	}
	for i := 0; i < len(segments); i++ {
		next, found, err := childFolder(ctx, tx, parent, segments[i])
		if err != nil {
			return 0, created, err
		}
		step := 1
		// Not a child by name: the longest run of the next levels that is already one folder's full path
		// (a literal name holding '/', or a folder elsewhere with that path) is taken whole.
		for j := len(segments); !found && j > i; j-- {
			if next, found, err = folderByPath(ctx, tx, join(path, segments[i:j]...)); err != nil {
				return 0, created, err
			}
			if found {
				step = j - i
			}
		}
		if !found {
			if next, err = createFolder(ctx, tx, parent, segments[i], -1); err != nil {
				return 0, created, err
			}
			created++
		}
		path = join(path, segments[i:i+step]...)
		parent = next
		i += step - 1
	}
	return parent, created, nil
}

// EnsureFolderChain returns the folder at the end of segments (one name per level, from the top),
// creating the missing ones at the end of the folder order: OPML import, where the structure is
// explicit and a '/' inside a name is just a character. No segments is the default folder.
func EnsureFolderChain(ctx context.Context, tx *sql.Tx, segments []string) (id int64, created int, err error) {
	if len(segments) == 0 {
		return 1, 0, nil
	}
	return walkFolders(ctx, tx, 0, segments)
}

// EnsureFolderChainFrom is EnsureFolderChain below parent (0 = the top level): an importer that has
// already resolved the upper levels does not walk them again.
func EnsureFolderChainFrom(ctx context.Context, tx *sql.Tx, parent int64, segments []string) (id int64, created int, err error) {
	if parent == 0 {
		return EnsureFolderChain(ctx, tx, segments)
	}
	if len(segments) == 0 {
		return parent, 0, nil
	}
	return walkFolders(ctx, tx, parent, segments)
}

// splitPath resolves a Reader API label path against the tree: the longest prefix of path (cut at a
// '/') that is an existing folder's full path becomes the parent, and the rest is split at '/' into the
// names below it. A literal top-level "A/B" therefore takes "A/B/C" as its child. No prefix: parent 0.
func splitPath(ctx context.Context, q Querier, path string) (parent int64, rest []string, err error) {
	for i := strings.LastIndexByte(path, '/'); i > 0; i = strings.LastIndexByte(path[:i], '/') {
		id, found, err := folderByPath(ctx, q, path[:i])
		if err != nil {
			return 0, nil, err
		}
		if found {
			return id, strings.Split(path[i+1:], "/"), nil
		}
	}
	return 0, strings.Split(path, "/"), nil
}

// resolveFolderPath returns the folder whose full path is path (ignoring ASCII case), creating it and
// any missing folder above it (splitPath) when there is none: the Reader API's subscribe and
// subscription/edit (a=user/-/label/Tech/Apple files the feed in Apple inside Tech). A blank path is
// the default folder.
func resolveFolderPath(ctx context.Context, tx *sql.Tx, path string) (int64, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 1, nil
	}
	if id, found, err := folderByPath(ctx, tx, path); err != nil || found {
		return id, err
	}
	parent, rest, err := splitPath(ctx, tx, path)
	if err != nil {
		return 0, err
	}
	id, _, err := walkFolders(ctx, tx, parent, rest)
	return id, err
}

// ---- the web UI ----

// CreateFolder adds a folder named name inside parent (0 = the top level); position < 0 puts it
// last. A taken path is ErrFolderExists; the tree rules are ErrFolderDepth and ErrFolderParent; a
// parent that does not exist is ErrParentNotFound.
func (d *DB) CreateFolder(ctx context.Context, name string, parent, position int64) (UIFolder, error) {
	var out UIFolder
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, err := createFolder(ctx, tx, parent, name, position)
		if err != nil {
			return err
		}
		out, err = uiFolder(ctx, tx, id)
		return err
	})
	return out, err
}

// FolderPatch is a validated PATCH /api/folders/{id}: nil fields stay as they are. Parent 0 is the top level.
type FolderPatch struct {
	Name     *string
	Parent   *int64
	Position *int64
}

// UpdateFolder renames, moves and/or repositions a folder. A path taken by another folder (the folder
// itself or one of its subfolders landing on it) is ErrFolderExists: the UI never merges; only the
// Reader API does. Moving inside itself is ErrFolderCycle, too deep ErrFolderDepth, under or of the
// default folder ErrFolderParent; a missing folder is ErrFolderNotFound, a missing parent ErrParentNotFound.
func (d *DB) UpdateFolder(ctx context.Context, id int64, p FolderPatch) (UIFolder, error) {
	var out UIFolder
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := loadFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Name != nil || p.Parent != nil {
			name, parent := cur.name, cur.parent
			if p.Name != nil {
				name = *p.Name
			}
			if p.Parent != nil {
				parent = *p.Parent
			}
			if err := placeAndSave(ctx, tx, id, parent, name); err != nil {
				return err
			}
			if parent != cur.parent {
				d.bumpFilters() // folder filters cover subfolders: the feeds a rule matches changed
			}
		}
		if p.Position != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE folders SET position = ? WHERE id = ?", *p.Position, id); err != nil {
				return err
			}
		}
		out, err = uiFolder(ctx, tx, id)
		return err
	})
	return out, err
}

// uiFolder reads one folder as the web UI shows it, with the unread count of its whole subtree.
func uiFolder(ctx context.Context, q Querier, id int64) (UIFolder, error) {
	out := UIFolder{ID: id}
	var parent sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT parent_id, name, position, is_default,
		(SELECT count(*) FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.folder_id IN (`+folderTreeSQL("?1")+`) AND i.read = 0 AND `+listedFeedSQL+`)
		FROM folders WHERE id = ?1`, id).Scan(&parent, &out.Name, &out.Position, &out.IsDefault, &out.Unread); err != nil {
		return out, err
	}
	if parent.Valid {
		out.ParentID = &parent.Int64
	}
	return out, nil
}

// DeleteFolder deletes a folder and every folder below it, moving all their feeds to the default
// folder; it returns the moved feeds' ids. The folders' filters go with them, and their favorites and
// saved-search scopes are dropped. The default folder cannot be deleted (ErrDefaultFolder). It is the
// delete of the web UI and of the Reader API's disable-tag alike.
func (d *DB) DeleteFolder(ctx context.Context, id int64) (moved []int64, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		moved = nil
		cur, err := loadFolder(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.isDefault {
			return ErrDefaultFolder
		}
		tree, err := queryIDs(ctx, tx, folderTreeSQL("?"), id)
		if err != nil {
			return err
		}
		if moved, err = queryIDs(ctx, tx, "UPDATE feeds SET folder_id = 1, updated_at = unixepoch() WHERE folder_id IN ("+
			folderTreeSQL("?")+") RETURNING id", id); err != nil {
			return err
		}
		for _, fid := range tree {
			if err := dropFavorite(ctx, tx, FavFolder, fid); err != nil {
				return err
			}
		}
		// The subfolders go through the parent_id cascade, their filters through filters.folder_id's.
		_, err = tx.ExecContext(ctx, "DELETE FROM folders WHERE id = ?", id)
		return err
	})
	if err == nil {
		d.bumpFilters() // the folders' own filters cascade away with them
	}
	return moved, err
}
