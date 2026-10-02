package backup

import "context"

// RevokeSessions deletes every web session in the database file at path
// (restore does this to the copy it is about to install), returning how many.
func RevokeSessions(ctx context.Context, path string) (int64, error) {
	db, err := openFile(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	res, err := db.ExecContext(ctx, "DELETE FROM sessions")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
