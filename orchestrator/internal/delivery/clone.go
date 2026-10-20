package delivery

import "strings"

// ShortCloneError is lux's clone error as one line: git's last fatal
// message when it has one (lux joins git's stderr after "git clone: exit
// status N: "), else the whole of it, bounded to 200 characters; "" for
// none.
func ShortCloneError(err string) string {
	short := strings.TrimSpace(err)
	if i := strings.LastIndex(short, "fatal: "); i >= 0 {
		short = short[i+len("fatal: "):]
	}
	short = strings.Join(strings.Fields(short), " ")
	if r := []rune(short); len(r) > 200 {
		short = string(r[:199]) + "…"
	}
	return short
}

// EvCheckoutRequested is a session's agent asking again for checkouts of
// its linked repositories (refresh_checkout). Payload: { project,
// repositories: [{ id, name }] }.
const EvCheckoutRequested = "session.checkout.requested"
