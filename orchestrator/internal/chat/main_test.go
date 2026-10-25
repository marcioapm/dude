package chat_test

import (
	"os"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
