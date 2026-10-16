package ops

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	PasswordIterations = 1000 // fast hashes in tests; production uses 600,000
	os.Exit(m.Run())
}
