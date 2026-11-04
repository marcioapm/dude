package delivery

// Bypass 2: the status moved to, through a local and a package var.
var zzParked = "awaiting_input"

func zzParkLocal(ctx context.Context, tx pgx.Tx, st *State) error {
	status := "awaiting_input"
	_, err := SetTaskStatusTx(ctx, tx, st.Org, st.ProjectID, st.TaskID, "", status, "parked")
	return err
}

func zzParkVar(ctx context.Context, tx pgx.Tx, st *State) error {
	_, err := SetTaskStatusTx(ctx, tx, st.Org, st.ProjectID, st.TaskID, "", zzParked, "parked")
	return err
}
