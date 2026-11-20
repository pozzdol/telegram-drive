// Package comment: comments on files, for everyone who may comment (owner, editors,
// commenters, and guests of a link that allows it). The file's owner gets a Telegram message.
package comment

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"telegram-drive/app"
	"telegram-drive/app/models"
	"telegram-drive/app/utils"
	"telegram-drive/app/utils/tgbot"
)

type Controller struct{ *app.App }

const maxBody = 2000 // characters

type comment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Guest     bool   `json:"guest"`
	Mine      bool   `json:"mine"`
	CanDelete bool   `json:"can_delete"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

// file checks uid's role on the file in the URL, writing 404 when it is below min.
func (c Controller) file(w http.ResponseWriter, r *http.Request, uid string, min models.Role) (string, models.Role, bool) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return "", 0, false
	}
	role, err := models.Access(r.Context(), c.DB, uid, "file", id)
	if err != nil || role < min {
		utils.WriteErr(w, http.StatusNotFound, "file not found")
		return "", 0, false
	}
	return id, role, true
}

func guestSession(r *http.Request) string {
	if g := models.GuestFrom(r.Context()); g != nil {
		return g.Session
	}
	return ""
}

// GET /api/files/{id}/comments — oldest first.
func (c Controller) List(w http.ResponseWriter, r *http.Request, uid string) {
	id, role, ok := c.file(w, r, uid, models.Viewer)
	if !ok {
		return
	}
	rows, err := c.DB.Query(`SELECT c.id, c.user_id IS NULL, coalesce(u.username, c.guest_name, 'Tamu'),
			(c.user_id = $2 AND $3 = '') OR (c.guest IS NOT NULL AND c.guest = $3), c.body, c.created_at
		FROM comments c LEFT JOIN users u ON u.id = c.user_id WHERE c.file_id = $1 ORDER BY c.created_at, c.id`,
		id, uid, guestSession(r))
	if err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []comment{}
	for rows.Next() {
		var x comment
		rows.Scan(&x.ID, &x.Guest, &x.Author, &x.Mine, &x.Body, &x.CreatedAt)
		x.CanDelete = x.Mine || role >= models.Editor
		out = append(out, x)
	}
	utils.WriteJSON(w, http.StatusOK, map[string]any{"comments": out, "can_comment": role >= models.Commenter})
}

// POST /api/files/{id}/comments {"body"} — needs the commenter role or higher.
func (c Controller) Create(w http.ResponseWriter, r *http.Request, uid string) {
	id, _, ok := c.file(w, r, uid, models.Commenter)
	if !ok {
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !utils.ReadJSON(w, r, &req) {
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" || utf8.RuneCountInString(body) > maxBody || !utf8.ValidString(body) {
		utils.WriteErr(w, http.StatusBadRequest, "comment must be 1-2000 characters")
		return
	}
	cid := models.NewID()
	var userID, guest any = uid, nil
	if g := models.GuestFrom(r.Context()); g != nil {
		userID, guest = nil, g.Session
	}
	now := time.Now().Unix()
	if _, err := c.DB.Exec(`INSERT INTO comments (id, file_id, user_id, guest_name, guest, body, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cid, id, userID, models.GuestName(r.Context()), guest, body, now); err != nil {
		utils.ServerErr(w, http.StatusInternalServerError, err)
		return
	}
	go c.notify(id, uid, models.GuestFrom(r.Context()), body)
	utils.WriteJSON(w, http.StatusCreated, comment{ID: cid, Body: body, CreatedAt: now, Mine: true, CanDelete: true})
}

// notify tells the file's owner about a comment from someone else, through the bot, when they
// linked their Telegram account and the bot is set up.
func (c Controller) notify(fileID, uid string, g *models.Guest, body string) {
	if c.BotToken == "" {
		return
	}
	var owner, name string
	var folder *string
	var chat *int64
	if err := c.DB.QueryRow(`SELECT f.user_id, f.name, f.folder_id, u.telegram_id FROM files f JOIN users u ON u.id = f.user_id WHERE f.id = $1`,
		fileID).Scan(&owner, &name, &folder, &chat); err != nil || chat == nil || (g == nil && owner == uid) {
		return
	}
	author := "Tamu"
	if g != nil {
		if g.Name != "" {
			author = g.Name
		}
		author += " (tamu)"
	} else {
		c.DB.QueryRow(`SELECT username FROM users WHERE id = $1`, uid).Scan(&author)
	}
	if utf8.RuneCountInString(body) > 500 {
		body = string([]rune(body)[:500]) + "…"
	}
	text := author + " berkomentar di “" + name + "”:\n\n" + body
	if c.Origin != "" {
		where := c.Origin + "/"
		if folder != nil {
			where += "?folder=" + *folder
		}
		text += "\n\nBuka: " + where
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := tgbot.Send(ctx, c.BotToken, *chat, text); err != nil {
		log.Printf("comment notification: %v", err) // e.g. 403: the owner has not allowed the bot to write
	}
}

// DELETE /api/comments/{id} — the author, or an editor of the file, may delete it.
func (c Controller) Delete(w http.ResponseWriter, r *http.Request, uid string) {
	id, ok := utils.PathID(w, r)
	if !ok {
		return
	}
	var fileID string
	var author, guest *string
	if err := c.DB.QueryRow(`SELECT file_id, user_id, guest FROM comments WHERE id = $1`, id).Scan(&fileID, &author, &guest); err != nil {
		utils.WriteErr(w, http.StatusNotFound, "comment not found")
		return
	}
	role, err := models.Access(r.Context(), c.DB, uid, "file", fileID)
	session := guestSession(r)
	mine := (session == "" && author != nil && *author == uid) || (session != "" && guest != nil && *guest == session)
	if err != nil || (!mine && role < models.Editor) {
		utils.WriteErr(w, http.StatusNotFound, "comment not found")
		return
	}
	c.DB.Exec(`DELETE FROM comments WHERE id = $1`, id)
	w.WriteHeader(http.StatusNoContent)
}
