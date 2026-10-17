package main

import (
	"os"
	"path/filepath"

	"github.com/marciomartins/dude/orchestrator/internal/refusalgen"
)

func main() {
	root := filepath.Join("..", "..")
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
