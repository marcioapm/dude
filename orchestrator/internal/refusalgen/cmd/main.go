// Command cmd writes the generated refusal files; its one argument is the orchestrator module root.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marciomartins/dude/orchestrator/internal/refusalgen"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: cmd <orchestrator module root>")
		os.Exit(2)
	}
	root := os.Args[1]
	out, err := refusalgen.Render(os.DirFS(root))
	if err != nil {
		panic(err)
	}
	for name, data := range out {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			panic(err)
		}
	}
}
