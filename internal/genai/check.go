package genai

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lockyc/plate/internal/pins"
)

// Check refuses a round model m cannot take: fewer than one attempt, more
// references than its MaxRefs, or a size outside its AllowedSizes. It is the
// one check of these; a caller runs it before claiming anything or sending a
// request, and every Provider's Generate takes a request it has passed.
func Check(m pins.ImageModel, n, refs int, size string) error {
	if n < 1 {
		return fmt.Errorf("%d attempts asked for; need at least 1", n)
	}
	if m.MaxRefs > 0 && refs > m.MaxRefs {
		return fmt.Errorf("%s takes at most %d references; %d given", m.ID, m.MaxRefs, refs)
	}
	if sizes := m.AllowedSizes(); !slices.Contains(sizes, size) {
		return fmt.Errorf("%s has no size %q (sizes: %s)", m.ID, size, strings.Join(sizes, ", "))
	}
	return nil
}
