package pins

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// ImageModel is one hosted image model `plate generate` can call. Exactly one
// billing shape is filled: PerImage (rate-billed, size -> USD), InPerMTok and
// OutPerMTok (usage-billed, USD per million tokens), FirstMP, ExtraMP and
// RefMP (megapixel-billed, USD), or Reported (the provider states each
// request's cost in its answer). Sizes are the size tiers the model takes;
// a rate-billed model leaves it empty, its rate table's keys being its sizes
// (AllowedSizes). Params are request fields the model needs as well.
// SizeForm is how an openrouter model is told its size: "" sends the
// resolution tier, "pixels" an explicit width and height. MaxPixels caps the
// pixels a model told explicit dimensions makes, and MaxEdge, when set,
// either side. AspectPixels is the fixed "WxH" a model that takes only a few
// sizes makes for each aspect.
type ImageModel struct {
	Provider, ID, KeyEnv    string
	PerImage                map[string]float64
	InPerMTok, OutPerMTok   float64
	FirstMP, ExtraMP, RefMP float64
	Reported                bool
	Sizes                   []string
	Params                  map[string]string
	MaxRefs                 int
	SizeForm                string
	MaxPixels               float64
	MaxEdge                 int
	AspectPixels            map[string]string
	Default                 bool
}

// AllowedSizes is the size tiers the model takes: Sizes, or a rate-billed
// model's rate table's keys, sorted.
func (m ImageModel) AllowedSizes() []string {
	if len(m.Sizes) > 0 || len(m.PerImage) == 0 {
		return m.Sizes
	}
	sizes := make([]string, 0, len(m.PerImage))
	for s := range m.PerImage {
		sizes = append(sizes, s)
	}
	sort.Strings(sizes)
	return sizes
}

// ImageModels is every model plate generate knows. The entry marked Default
// is the model a run without --provider uses; otherwise the first entry for
// each provider is that provider's default.
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
	// the design spec's, not confirmed on a docs page. AspectPixels are the
	// sizes the edit endpoint documents as recommended; 1K and 2K both use
	// them, and 4K is not offered, its sizes documented as experimental above
	// 2560x1440.
	{Provider: "openai", ID: "gpt-image-2", KeyEnv: "OPENAI_API_KEY",
		InPerMTok: 8, OutPerMTok: 30, MaxRefs: 16, Sizes: []string{"1K", "2K"},
		AspectPixels: map[string]string{"1:1": "1024x1024", "3:2": "1536x1024", "2:3": "1024x1536"}},
	// Checked 2026-10-24: the ID is BFL's pinned FLUX.2 [pro] snapshot; the
	// rates are the design spec's, not published in the docs text. Four
	// megapixels are the most BFL documents for an output, so there is no 4K.
	{Provider: "bfl", ID: "flux-2-pro", KeyEnv: "BFL_API_KEY",
		FirstMP: 0.03, ExtraMP: 0.015, RefMP: 0.015, MaxRefs: 8, Sizes: []string{"1K", "2K"}, MaxPixels: 4e6},
	// Checked 2026-10-24: the ID is documented at POST /v1/flux-2-max in
	// BFL's API reference; the rates are the design spec's.
	{Provider: "bfl", ID: "flux-2-max", KeyEnv: "BFL_API_KEY",
		FirstMP: 0.07, ExtraMP: 0.03, RefMP: 0.03, MaxRefs: 8, Sizes: []string{"1K", "2K"}, MaxPixels: 4e6},
	// Checked 2026-10-24 against OpenRouter's GET /api/v1/images/models: the
	// IDs, the resolutions each states (FLUX.2 and GPT Image state none), the
	// input_references maximum, and the output_format and quality each takes.
	// Size forms measured live 2026-10-24 at 2K with a 520x620 reference:
	// FLUX.2 and GPT Image ignore resolution (FLUX.2 returns the reference's
	// size) and honour an explicit size exactly; Gemini takes the tier.
	{Provider: "openrouter", ID: "google/gemini-3.1-flash-image", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Sizes: []string{"512", "1K", "2K", "4K"}, MaxRefs: 14},
	// Measured 2026-10-24: returns 1K pixels at resolution 2K, with or
	// without aspect_ratio.
	{Provider: "openrouter", ID: "google/gemini-3-pro-image", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Sizes: []string{"1K", "2K", "4K"}, MaxRefs: 14},
	// Checked 2026-10-24 (OpenAI's image generation guide): sides multiples
	// of 16, neither over 3840, at most 8,294,400 pixels.
	// The overall default, 2026-10-24: on a character no model had seen, it
	// kept the character together across varied poses best (docs/roadmap.md).
	{Provider: "openrouter", ID: "openai/gpt-image-2", KeyEnv: "OPENROUTER_API_KEY",
		Default: true, Reported: true, Params: map[string]string{"quality": "high"}, MaxRefs: 16,
		SizeForm: "pixels", Sizes: []string{"1K", "2K", "4K"}, MaxPixels: 8294400, MaxEdge: 3840},
	// FLUX.2 makes at most 4 MP (BFL's docs), so there is no 4K.
	{Provider: "openrouter", ID: "black-forest-labs/flux.2-pro", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Params: map[string]string{"output_format": "png"}, MaxRefs: 8,
		SizeForm: "pixels", Sizes: []string{"1K", "2K"}, MaxPixels: 4e6},
	{Provider: "openrouter", ID: "black-forest-labs/flux.2-max", KeyEnv: "OPENROUTER_API_KEY",
		Reported: true, Params: map[string]string{"output_format": "png"}, MaxRefs: 8,
		SizeForm: "pixels", Sizes: []string{"1K", "2K"}, MaxPixels: 4e6},
}

// DefaultImageModel returns the model a run without --provider uses.
func DefaultImageModel() ImageModel {
	for _, m := range ImageModels {
		if m.Default {
			return m
		}
	}
	return ImageModel{}
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
