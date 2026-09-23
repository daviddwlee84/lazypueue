//go:build !windows

package pueue

import (
	"os"
	"testing"
)

func writeFakeScript(t *testing.T, path, script string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
