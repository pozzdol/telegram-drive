package models

import (
	"context"
	"database/sql"
	"time"
)

// Guest is someone who opened a public link without logging in. Handlers get GuestUID as their
// uid (a valid UUID that matches no account) and the Guest in the request context.
type Guest struct {
	Session    string // guest session token hash
	ItemID     string // the shared file or folder; the guest sees it and everything inside
	Role       Role
	Name       string // typed by the guest, may be empty
	NoDownload bool   // viewers may only preview
}

// GuestUID stands in for a user id in requests made by guests.
const GuestUID = "00000000-0000-0000-0000-000000000000"

type guestKey struct{}

func WithGuest(ctx context.Context, g *Guest) context.Context {
	return context.WithValue(ctx, guestKey{}, g)
}

// GuestFrom returns the guest making this request, or nil for a logged-in user.
func GuestFrom(ctx context.Context) *Guest {
	g, _ := ctx.Value(guestKey{}).(*Guest)
	return g
}

// Actor is the value for "who did it" columns (trashed_by, created_by): NULL for guests.
func Actor(ctx context.Context, uid string) any {
	if GuestFrom(ctx) != nil {
		return nil
	}
	return uid
}

// OwnerFor is who owns a new folder or file created by uid inside folder ref: uid itself, or
// for a guest the owner of the folder (guests have no account to own anything).
func OwnerFor(ctx context.Context, db *sql.DB, uid string, ref any) (string, error) {
	if GuestFrom(ctx) == nil {
		return uid, nil
	}
	var owner string
	err := db.QueryRowContext(ctx, `SELECT user_id FROM folders WHERE id = $1`, ref).Scan(&owner)
	return owner, err
}

// GuestName is the name to record for what a guest adds, or NULL for users.
func GuestName(ctx context.Context) any {
	if g := GuestFrom(ctx); g != nil {
		if g.Name == "" {
			return "Tamu"
		}
		return g.Name
	}
	return nil
}

// LoadGuest finds the guest session with this token hash, if its link still works.
func LoadGuest(ctx context.Context, db *sql.DB, session string) (*Guest, error) {
	g := &Guest{Session: session}
	var role string
	err := db.QueryRowContext(ctx, `SELECT s.item_id, s.name, l.role, l.no_download
		FROM guest_sessions s JOIN links l ON l.item_id = s.item_id AND l.token = s.link_token
		WHERE s.token_hash = $1 AND s.expires_at > $2 AND l.enabled AND (l.expires_at IS NULL OR l.expires_at > $2)`,
		session, time.Now().Unix()).Scan(&g.ItemID, &g.Name, &role, &g.NoDownload)
	if err != nil {
		return nil, err
	}
	g.Role, _ = ParseShareRole(role)
	return g, nil
}
