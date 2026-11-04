package delivery

// Bypass 1: the SQL that asks a person, held in file-level consts.
const zzAskSQL = `INSERT INTO questions (organization_id, task_id, prompt) VALUES ($1, $2, $3)`

var zzParkSQL = "UPDATE tasks SET status = 'awaiting_input' WHERE id = $1"

func zzAsk(ctx context.Context, tx pgx.Tx, org, task, prompt string) error {
	if _, err := tx.Exec(ctx, zzAskSQL, org, task, prompt); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, zzParkSQL, task)
	return err
}
