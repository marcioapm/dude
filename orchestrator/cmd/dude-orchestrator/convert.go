package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// convertDeliveries is the boot's one-shot sweep: every live delivery no
// conductor decides goes to a quiet one (delivery.ConvertInFlight), its
// counts logged once. A failure is logged and boot goes on: the next boot,
// or `dude-orchestrator convert-deliveries`, sweeps again.
func convertDeliveries(ctx context.Context, database *db.DB, log *slog.Logger) {
	pass, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	n, err := delivery.ConvertInFlight(pass, database)
	attrs := []any{"converted", n.Converted, "skipped_accepted", n.Accepted, "skipped_moving", n.Moving}
	if err != nil {
		log.Error("deliveries in flight not all handed to a quiet conductor; the next boot sweeps again",
			append(attrs, "error", err)...)
		return
	}
	log.Info("deliveries in flight handed to a quiet conductor", attrs...)
}

// convertCommand is `dude-orchestrator convert-deliveries`: the boot's
// sweep, run on demand for a rolling deploy, whose old binaries may start
// deliveries after the new one booted. Exit 0 with the counts on stdout,
// 1 on a failure, 2 on extra arguments.
func convertCommand(ctx context.Context, args []string, opts config.Options, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: dude-orchestrator convert-deliveries")
		return 2
	}
	_, set, err := resolve(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	database, err := db.Open(ctx, set.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer database.Close()
	n, err := delivery.ConvertInFlight(ctx, database)
	fmt.Fprintf(stdout, "converted %d, skipped (accepted) %d, skipped (moving) %d\n", n.Converted, n.Accepted, n.Moving)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
