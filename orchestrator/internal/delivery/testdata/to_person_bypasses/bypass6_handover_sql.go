package delivery

// Bypass 6: a conductor's question addressed to a person in place.
func zzHandOver(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE questions SET addressee = 'person' WHERE id = $1`, id)
	return err
}
