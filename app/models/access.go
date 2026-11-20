package models

import (
	"context"
	"database/sql"
	"errors"
)

// Role is what a user may do with a file or folder. Higher includes lower.
type Role int

const (
	None      Role = iota
	Viewer         // open and download
	Commenter      // view, and comment
	Editor         // upload, create folders, rename, move, delete, share
	Owner          // the item is theirs
)

var roleNames = map[Role]string{Viewer: "viewer", Commenter: "commenter", Editor: "editor", Owner: "owner"}

func (r Role) String() string { return roleNames[r] }

// ParseShareRole reads a role that can be given to someone ("viewer", "commenter", "editor").
func ParseShareRole(s string) (Role, bool) {
	for r, n := range roleNames {
		if n == s && r != Owner {
			return r, true
		}
	}
	return None, false
}

// ErrNoAccess: the item does not exist, is in the trash, or uid may not see it. Callers answer
// 404 for all three, so nobody learns which ids exist.
var ErrNoAccess = errors.New("not found")

// Access works out uid's role on a file or folder (kind "file" or "folder"):
//   - Owner when the item is uid's,
//   - Editor when one of the folders around it is uid's (someone else uploaded into it),
//   - otherwise the highest role shared with uid on the item or on any folder around it.
//
// Items in the trash give no access here; the trash has its own endpoints.
func Access(ctx context.Context, db *sql.DB, uid, kind, id string) (Role, error) {
	if !ValidID(id) {
		return None, ErrNoAccess
	}
	var owner string
	var parent *string
	var trashed bool
	q := `SELECT user_id, parent_id, trashed_at IS NOT NULL FROM folders WHERE id = $1`
	if kind == "file" {
		q = `SELECT user_id, folder_id, trashed_at IS NOT NULL FROM files WHERE id = $1`
	}
	if err := db.QueryRowContext(ctx, q, id).Scan(&owner, &parent, &trashed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return None, ErrNoAccess
		}
		return None, err
	}
	if trashed {
		return None, ErrNoAccess
	}
	g := GuestFrom(ctx)
	if owner == uid && g == nil {
		return Owner, nil
	}
	best := None
	ids := []string{id}
	if parent != nil {
		rows, err := db.QueryContext(ctx, `WITH RECURSIVE up(id, parent_id, user_id) AS (
				SELECT id, parent_id, user_id FROM folders WHERE id = $1
				UNION ALL SELECT f.id, f.parent_id, f.user_id FROM folders f JOIN up ON f.id = up.parent_id
			) SELECT id, user_id FROM up`, *parent)
		if err != nil {
			return None, err
		}
		for rows.Next() {
			var fid, fowner string
			rows.Scan(&fid, &fowner)
			ids = append(ids, fid)
			if fowner == uid {
				best = Editor
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return None, err
		}
	}
	if g != nil {
		// A guest sees the linked item and whatever is inside it, with the link's role.
		for _, x := range ids {
			if x == g.ItemID {
				return g.Role, nil
			}
		}
		return None, ErrNoAccess
	}
	rows, err := db.QueryContext(ctx, `SELECT role FROM shares WHERE user_id = $1 AND item_id = ANY($2::uuid[])`, uid, ids)
	if err != nil {
		return None, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		rows.Scan(&name)
		if r, ok := ParseShareRole(name); ok && r > best {
			best = r
		}
	}
	if best == None {
		return None, ErrNoAccess
	}
	return best, rows.Err()
}

// TreeOwner is the owner of the top folder above an item (or of the item itself at a root).
// Editors may move other people's items only within the same tree.
func TreeOwner(ctx context.Context, db *sql.DB, kind, id string) (string, error) {
	start := `SELECT parent_id, user_id FROM folders WHERE id = $1`
	if kind == "file" {
		start = `SELECT folder_id, user_id FROM files WHERE id = $1`
	}
	var parent *string
	var owner string
	if err := db.QueryRowContext(ctx, start, id).Scan(&parent, &owner); err != nil {
		return "", err
	}
	if parent == nil {
		return owner, nil
	}
	err := db.QueryRowContext(ctx, `WITH RECURSIVE up(id, parent_id, user_id) AS (
			SELECT id, parent_id, user_id FROM folders WHERE id = $1
			UNION ALL SELECT f.id, f.parent_id, f.user_id FROM folders f JOIN up ON f.id = up.parent_id
		) SELECT user_id FROM up WHERE parent_id IS NULL`, *parent).Scan(&owner)
	return owner, err
}

// CanMove reports whether uid may move an item into target ("" = uid's own root): uid must be
// an editor of both, and someone else's item may only move within the tree it is in.
// Errors: ErrNoAccess for the item, ErrFolderNotFound for the target.
func CanMove(ctx context.Context, db *sql.DB, uid, kind, id, target string) error {
	r, err := Access(ctx, db, uid, kind, id)
	if err != nil || r < Editor {
		return ErrNoAccess
	}
	if _, err := FolderRef(ctx, db, uid, target, Editor); err != nil {
		return ErrFolderNotFound
	}
	if r == Owner {
		return nil
	}
	if GuestFrom(ctx) != nil && target == "" {
		return ErrFolderNotFound // a guest has no drive of their own
	}
	from, err := TreeOwner(ctx, db, kind, id)
	if err != nil {
		return err
	}
	to := uid
	if target != "" {
		if to, err = TreeOwner(ctx, db, "folder", target); err != nil {
			return err
		}
	}
	if from != to {
		return ErrNoAccess
	}
	return nil
}

// LogAccess records a change to who can open an item (shares, link settings).
func LogAccess(ctx context.Context, db *sql.DB, uid, item, action string) {
	actor := "Tamu"
	if g := GuestFrom(ctx); g != nil {
		if g.Name != "" {
			actor = g.Name
		}
		actor += " (tamu)"
	} else {
		db.QueryRowContext(ctx, `SELECT username FROM users WHERE id = $1`, uid).Scan(&actor)
	}
	db.ExecContext(ctx, `INSERT INTO access_log (item_id, actor, action) VALUES ($1, $2, $3)`, item, actor, action)
}
