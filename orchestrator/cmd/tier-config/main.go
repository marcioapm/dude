// Command tier-config prints the OPENCODE_CONFIG_CONTENT dude gives a
// phase Run on a tier, exactly as buildSpec makes it, for
// scripts/real-thinking.sh.
//
//	tier-config MODEL [EFFORT]
package main

import (
	"fmt"
	"os"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tier-config MODEL [EFFORT]")
		os.Exit(2)
	}
	tier := delivery.Tier{Name: "Probe", Model: os.Args[1]}
	if len(os.Args) > 2 {
		tier.Effort = os.Args[2]
	}
	fmt.Println(phases.OpenCodeConfigFor(tier))
}
