package delivery

// Bypass 5: parameterised SQL; awaiting_input is an argument.
func zzParkParam(ctx context.Context, tx pgx.Tx, task string) error {
	_, err := tx.Exec(ctx, `UPDATE tasks SET status = $2::task_status WHERE id = $1`, task, "awaiting_input")
	return err
}
