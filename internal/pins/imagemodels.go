package pins

import (
	"fmt"
	"os"
	"strings"
)

// ImageModel is one hosted image model `plate generate` can call. Exactly one
// billing shape is filled: PerImage (rate-billed, size -> USD), InPerMTok and
// OutPerMTok (usage-billed, USD per million tokens), FirstMP, ExtraMP and
// RefMP (megapixel-billed, USD), or Reported (the provider states each
// request's cost in its answer). Sizes, when set, are the only sizes the
// model takes; Params are request fields the model needs as well.
type ImageModel struct {
	Provider, ID, KeyEnv    string
	PerImage                map[string]float64
	InPerMTok, OutPerMTok   float64
	FirstMP, ExtraMP, RefMP float64
	Reported                bool
	Sizes                   []string
	Params                  map[string]string
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
	// Checked 2026-10-24: the ID is documented at POST /v1/flux-2-max in
	// BFL's API reference; the rates are the design spec's.
	{Provider: "bfl", ID: "flux-2-max", KeyEnv: "BFL_API_KEY",
		FirstMP: 0.07, ExtraMP: 0.03, RefMP: 0.03, MaxRefs: 8},
	// Checked 2026-10-24 against OpenRouter's GET /api/v1/images/models: the
	// IDs, the resolutions each states (FLUX.2 and GPT Image state none), the
	// input_references maximum, and the output_format and quality each takes.
	{Provider: "openrouter", ID: "google/gemini-3.1-flash-image", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Sizes: []string{"512", "1K", "2K", "4K"}, MaxRefs: 14},
	{Provider: "openrouter", ID: "google/gemini-3-pro-image", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Sizes: []string{"1K", "2K", "4K"}, MaxRefs: 14},
	{Provider: "openrouter", ID: "openai/gpt-image-2", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Params: map[string]string{"quality": "high"}, MaxRefs: 16},
	{Provider: "openrouter", ID: "black-forest-labs/flux.2-pro", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Params: map[string]string{"output_format": "png"}, MaxRefs: 8},
	{Provider: "openrouter", ID: "black-forest-labs/flux.2-max", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Params: map[string]string{"output_format": "png"}, MaxRefs: 8},
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
