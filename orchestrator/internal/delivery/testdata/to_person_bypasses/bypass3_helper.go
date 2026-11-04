package delivery

// Bypass 3: a helper wraps SetTaskStatusTx; the status is its caller's.
func zzSet(ctx context.Context, tx pgx.Tx, st *State, status string) error {
	_, err := SetTaskStatusTx(ctx, tx, st.Org, st.ProjectID, st.TaskID, "", status, "parked")
	return err
}

func zzPark(ctx context.Context, tx pgx.Tx, st *State) error {
	return zzSet(ctx, tx, st, "awaiting_input")
}
