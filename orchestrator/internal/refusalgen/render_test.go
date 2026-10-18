package refusalgen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedRefusalsAreCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	got, err := Render(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range got {
		have, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(have) != string(want) {
			t.Errorf("%s is stale", name)
		}
	}
}
