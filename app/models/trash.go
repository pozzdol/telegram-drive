package models

import (
	"context"
	"database/sql"
	"time"
)

// TrashDays is how long deleted items stay in the trash before they are purged for good.
const TrashDays = 30

// Trash moves files and folders, with everything inside the folders, to the trash. Each picked
// item starts its own group (trash_id = its id), restored or purged as one. Items already in
// the trash, or not owned by uid, are skipped. Returns how many files and folders were moved.
func Trash(ctx context.Context, db *sql.DB, uid string, files, folders []string) (nf, nd int64, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, top := range folders {
		// The folder and its subfolders that are not in the trash yet. A subfolder trashed
		// earlier keeps its own group, so it does not come back when this one is restored.
		res, err := tx.Exec(`WITH RECURSIVE t(id) AS (
				SELECT id FROM folders WHERE id = $1 AND user_id = $2 AND trashed_at IS NULL
				UNION SELECT f.id FROM folders f JOIN t ON f.parent_id = t.id WHERE f.trashed_at IS NULL
			) UPDATE folders SET trashed_at = $3, trash_id = $1, trashed_by = $2 WHERE id IN (SELECT id FROM t)`, top, uid, now)
		if err != nil {
			return 0, 0, err
		}
		n, _ := res.RowsAffected()
		nd += n
		res, err = tx.Exec(`UPDATE files SET trashed_at = $2, trash_id = $1, trashed_by = $3
			WHERE trashed_at IS NULL AND folder_id IN (SELECT id FROM folders WHERE trash_id = $1)`, top, now, uid)
		if err != nil {
			return 0, 0, err
		}
		n, _ = res.RowsAffected()
		nf += n
	}
	res, err := tx.Exec(`UPDATE files SET trashed_at = $3, trash_id = id, trashed_by = $1
		WHERE user_id = $1 AND id = ANY($2::uuid[]) AND trashed_at IS NULL`, uid, files, now)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	nf += n
	return nf, nd, tx.Commit()
}

// Restore brings trash groups back. An item whose folder is still in the trash (or gone)
// comes back at the root instead.
func Restore(ctx context.Context, db *sql.DB, uid string, groups []string) (n int64, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`UPDATE folders SET trashed_at = NULL, trash_id = NULL, trashed_by = NULL WHERE user_id = $1 AND trash_id = ANY($2::uuid[])`,
		`UPDATE files SET trashed_at = NULL, trash_id = NULL, trashed_by = NULL WHERE user_id = $1 AND trash_id = ANY($2::uuid[])`,
	} {
		res, err := tx.Exec(q, uid, groups)
		if err != nil {
			return 0, err
		}
		k, _ := res.RowsAffected()
		n += k
	}
	for _, q := range []string{
		`UPDATE folders SET parent_id = NULL WHERE user_id = $1 AND id = ANY($2::uuid[])
			AND parent_id IN (SELECT id FROM folders WHERE trashed_at IS NOT NULL)`,
		`UPDATE files SET folder_id = NULL WHERE user_id = $1 AND id = ANY($2::uuid[])
			AND folder_id IN (SELECT id FROM folders WHERE trashed_at IS NOT NULL)`,
	} {
		if _, err := tx.Exec(q, uid, groups); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}
