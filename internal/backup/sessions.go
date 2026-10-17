package backup

import "context"

// RevokeSessions deletes every web session in the database file at path
// (restore does this to the copy it is about to install), returning how many.
// The file came from outside, so it is opened like every other restore step,
// with trusted_schema off.
func RevokeSessions(ctx context.Context, path string) (int64, error) {
	db, err := openUntrusted(path)
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
