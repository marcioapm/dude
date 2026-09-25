// Package ids mints the prefixed, sortable identifiers the whole system uses.
//
// Must stay identical to packages/domain/src/ids.ts: both processes write the
// same tables, and an id that sorts or reads differently depending on which
// one made it would break the one property ids are for.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Prefixes, as in ID_PREFIXES. Only the kinds the orchestrator creates.
const (
	Run            = "run"
	Event          = "evt"
	WorkflowRun    = "wfr"
	WorkflowSignal = "sig"
	PullRequest    = "pr"
	Finding        = "find"
	Directive      = "dir"
	Question       = "qst"
	Artifact       = "art"
	WorkItem       = "wi"
	Epic           = "epc"
	RepoRequest    = "rrq"
)

// New returns `<prefix>_<base36 millis, 9 wide><16 hex>`.
func New(prefix string) string {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 36)
	ts = strings.Repeat("0", max(0, 9-len(ts))) + ts
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + ts + hex.EncodeToString(b[:])
}
