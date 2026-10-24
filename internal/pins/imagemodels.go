package pins

import (
	"fmt"
	"os"
	"strings"
)

// ImageModel is one hosted image model `plate generate` can call. Exactly one
// billing shape is filled: PerImage (rate-billed, size -> USD), InPerMTok and
// OutPerMTok (usage-billed, USD per million tokens), or FirstMP, ExtraMP and
// RefMP (megapixel-billed, USD).
type ImageModel struct {
	Provider, ID, KeyEnv    string
	PerImage                map[string]float64
	InPerMTok, OutPerMTok   float64
	FirstMP, ExtraMP, RefMP float64
	MaxRefs                 int
}

// ImageModels is every model plate generate knows. The first entry for each
// provider is that provider's default.
var ImageModels = []ImageModel{
	// Checked 2026-10-24 (Gemini pricing page).
	{Provider: "google", ID: "gemini-3.1-flash-image", KeyEnv: "GEMINI_API_KEY",
		PerImage: map[string]float64{"1K": 0.067, "2K": 0.101, "4K": 0.151}, MaxRefs: 14},
	// Checked 2026-10-24 (Gemini pricing page).
	{Provider: "google", ID: "gemini-nano-banana-2.1", KeyEnv: "GEMINI_API_KEY",
		PerImage: map[string]float64{"1K": 0.0336, "2K": 0.0504, "4K": 0.113}, MaxRefs: 14},
	// Checked 2026-10-24 (Gemini pricing page).
	{Provider: "google", ID: "gemini-3-pro-image", KeyEnv: "GEMINI_API_KEY",
		PerImage: map[string]float64{"1K": 0.134, "2K": 0.134, "4K": 0.24}, MaxRefs: 14},
	// Checked 2026-10-24: the ID is listed on OpenAI's model page; the rates are
	// the design spec's, not confirmed on a docs page.
	{Provider: "openai", ID: "gpt-image-2", KeyEnv: "OPENAI_API_KEY",
		InPerMTok: 8, OutPerMTok: 30, MaxRefs: 16},
	// Checked 2026-10-24: the ID is BFL's pinned FLUX.2 [pro] snapshot; the
	// rates are the design spec's, not published in the docs text.
	{Provider: "bfl", ID: "flux-2-pro", KeyEnv: "BFL_API_KEY",
		FirstMP: 0.03, ExtraMP: 0.015, RefMP: 0.015, MaxRefs: 8},
	// Checked 2026-10-24: BFL's docs text names no ID for [max]; the ID and
	// rates are the design spec's.
	{Provider: "bfl", ID: "flux-2-max", KeyEnv: "BFL_API_KEY",
		FirstMP: 0.07, ExtraMP: 0.03, RefMP: 0.03, MaxRefs: 8},
}

// ImageModelFor returns provider's model id; an empty id gives the provider's
// default.
func ImageModelFor(provider, id string) (ImageModel, error) {
	var ids []string
	for _, m := range ImageModels {
		if m.Provider != provider {
			continue
		}
		if id == "" || m.ID == id {
			return m, nil
		}
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return ImageModel{}, fmt.Errorf("unknown provider %q", provider)
	}
	return ImageModel{}, fmt.Errorf("unknown %s model %q; known: %s", provider, id, strings.Join(ids, ", "))
}

// Key reads the model's API key from the environment. The value is never
// part of an error.
func (m ImageModel) Key() (string, error) {
	if k := os.Getenv(m.KeyEnv); k != "" {
		return k, nil
	}
	return "", fmt.Errorf("%s is not set; %s needs an API key", m.KeyEnv, m.Provider)
}
