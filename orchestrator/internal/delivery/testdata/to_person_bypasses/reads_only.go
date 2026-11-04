package delivery

// Reads: none of these reaches a person, and the scan must say so.
func zzReads(ctx context.Context, tx pgx.Tx, st *State, d *Decision) (bool, error) {
	if d.TaskStatus != "awaiting_input" || "awaiting_input" == st.Status {
		return false, nil
	}
	switch st.Status {
	case "awaiting_input":
	}
	if _, err := SetTaskStatusTx(ctx, tx, st.Org, st.ProjectID, st.TaskID, "awaiting_input", "running", "answered"); err != nil {
		return false, err
	}
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE id = $1 AND status = $2`, st.TaskID, "awaiting_input").Scan(&n)
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE status = 'awaiting_input'`)
	return n > 0, err
}
