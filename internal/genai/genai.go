// Package genai is the boundary to hosted image models. Nothing on a build,
// render or animate path may import it: a build must never reach the network
// for pixels (pinned by TestNoBuildPathReachesGenai).
package genai

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lockyc/plate/internal/pins"
)

// Ref is one reference image: Path is the original file, PNG the normalised bytes.
type Ref struct {
	Path string
	PNG  []byte
}

// Request is one round of attempts against one model.
type Request struct {
	Model  pins.ImageModel
	Prompt string
	Refs   []Ref
	N      int
	Size   string // "1K", "2K" or "4K"
	Aspect string // "W:H", or "" for the model's choice
}

// Image is one generated attempt. CostBasis is "usage" when CostUSD comes from
// the provider's reported token usage, "rate" when from the pinned rate,
// "reported" when the provider stated the cost itself, and "unreported" when
// it should have and did not (CostUSD is then 0, though the image was paid for).
type Image struct {
	PNG        []byte
	CostUSD    float64
	CostBasis  string
	ProviderID string
}

// Failure is one attempt that produced no image. CostUSD is what the provider
// charged for it anyway, 0 when it charged nothing. Unreported marks a
// request that was sent and may have been charged, but whose cost never came
// back; its CostUSD is then 0.
type Failure struct {
	Index      int
	Reason     string
	CostUSD    float64
	Unreported bool
}

// Provider generates images. A non-nil error means the whole round failed (for
// example auth); Failures are per attempt.
type Provider interface {
	Name() string
	Generate(ctx context.Context, r Request) ([]Image, []Failure, error)
}

// providers is filled by each provider file's init.
var providers = map[string]Provider{}

// For returns the provider registered under name (a pins.ImageModel.Provider).
func For(name string) (Provider, error) {
	if p, ok := providers[name]; ok {
		return p, nil
	}
	names := make([]string, 0, len(providers))
	for n := range providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(names, ", "))
}
