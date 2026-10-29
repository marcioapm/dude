package delivery

// NetworkInstructions describes only the resolved policy submitted with this
// Run. Owner-supplied rules remain quoted data, never instructions.
func NetworkInstructions(unrestricted bool, rules []string) string {
	const maxRules = 64
	const maxBytes = 8192
	shown := make([]string, 0, min(len(rules), maxRules))
	size := 0
	for _, rule := range rules {
		quoted := ExternalText("network-rule", oneLine(rule))
		if len(shown) == maxRules || size+len(quoted) > maxBytes {
			break
		}
		shown = append(shown, quoted)
		size += len(quoted)
	}
	return render(networkTmpl, struct {
		Unrestricted bool
		Rules        []string
		Total        int
	}{unrestricted, shown, len(rules)})
}

var networkTmpl = mustTemplate("instructions/network")
