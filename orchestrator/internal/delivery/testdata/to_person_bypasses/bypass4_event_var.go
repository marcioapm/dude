package delivery

// Bypass 4: question.asked's type through a local.
func zzEmit(ctx context.Context, tx pgx.Tx, st *State) error {
	typ := EvQuestionAsked
	return emitTx(ctx, tx, "", st, typ, nil)
}

func zzAppend(ctx context.Context, tx pgx.Tx, r RunRef) error {
	typ := EvQuestionAsked
	_, err := ledger.Append(ctx, tx, r.Event(typ, ledger.ActorAgent, nil))
	return err
}
