package models

import (
	"context"
	"database/sql"
	"time"
)

// TrashDays is how long deleted items stay in the trash before they are purged for good.
const TrashDays = 30

// Trash moves files and folders, with everything inside the folders, to the trash of their
// owner. Each picked item starts its own group (trash_id = its id), restored or purged as one.
// The caller checks that uid may delete them (editor); uid is recorded as who deleted them.
// Items already in the trash are skipped. Returns how many files and folders were moved.
func Trash(ctx context.Context, db *sql.DB, by any, files, folders []string) (nf, nd int64, err error) {
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
				SELECT id FROM folders WHERE id = $1 AND trashed_at IS NULL
				UNION SELECT f.id FROM folders f JOIN t ON f.parent_id = t.id WHERE f.trashed_at IS NULL
			) UPDATE folders SET trashed_at = $3, trash_id = $1, trashed_by = $2 WHERE id IN (SELECT id FROM t)`, top, by, now)
		if err != nil {
			return 0, 0, err
		}
		n, _ := res.RowsAffected()
		nd += n
		res, err = tx.Exec(`UPDATE files SET trashed_at = $2, trash_id = $1, trashed_by = $3
			WHERE trashed_at IS NULL AND folder_id IN (SELECT id FROM folders WHERE trash_id = $1)`, top, now, by)
		if err != nil {
			return 0, 0, err
		}
		n, _ = res.RowsAffected()
		nf += n
	}
	res, err := tx.Exec(`UPDATE files SET trashed_at = $3, trash_id = id, trashed_by = $1
		WHERE id = ANY($2::uuid[]) AND trashed_at IS NULL`, by, files, now)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	nf += n
	// A document's pictures go with it, in its group.
	for _, q := range []string{
		`UPDATE folders SET trashed_at = $2, trash_id = assets_of, trashed_by = $3 WHERE assets_of = ANY($1::uuid[]) AND trashed_at IS NULL`,
		`UPDATE files SET trashed_at = $2, trash_id = (SELECT assets_of FROM folders d WHERE d.id = files.folder_id), trashed_by = $3
			WHERE trashed_at IS NULL AND folder_id IN (SELECT id FROM folders WHERE assets_of = ANY($1::uuid[]))`,
	} {
		if _, err := tx.Exec(q, files, now, by); err != nil {
			return 0, 0, err
		}
	}
	return nf, nd, tx.Commit()
}

// Restore brings trash groups back (the caller checks uid may). An item whose folder is still in
// the trash comes back at the root of its owner instead.
func Restore(ctx context.Context, db *sql.DB, groups []string) (n int64, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`UPDATE folders SET trashed_at = NULL, trash_id = NULL, trashed_by = NULL WHERE trash_id = ANY($1::uuid[])`,
		`UPDATE files SET trashed_at = NULL, trash_id = NULL, trashed_by = NULL WHERE trash_id = ANY($1::uuid[])`,
	} {
		res, err := tx.Exec(q, groups)
		if err != nil {
			return 0, err
		}
		k, _ := res.RowsAffected()
		n += k
	}
	for _, q := range []string{
		`UPDATE folders SET parent_id = NULL WHERE id = ANY($1::uuid[])
			AND parent_id IN (SELECT id FROM folders WHERE trashed_at IS NOT NULL)`,
		`UPDATE files SET folder_id = NULL WHERE id = ANY($1::uuid[])
			AND folder_id IN (SELECT id FROM folders WHERE trashed_at IS NOT NULL)`,
	} {
		if _, err := tx.Exec(q, groups); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}
