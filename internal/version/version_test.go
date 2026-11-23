package version_test

import (
	"strings"
	"testing"

	"github.com/mareklanduch/gomada/internal/version"
)

func TestString(t *testing.T) {
	got := version.String()
	for _, want := range []string{"gomada", version.Version, version.Commit, version.Date} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}
