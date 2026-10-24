package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"net/http"
	"slices"
	"strings"
)

const openrouterDefaultURL = "https://openrouter.ai"

type openrouter struct{}

func init() { providers["openrouter"] = openrouter{} }

func (openrouter) Name() string { return "openrouter" }

type openrouterRef struct {
	Type     string `json:"type"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type openrouterResponse struct {
	Data []struct {
		B64       string `json:"b64_json"`
		MediaType string `json:"media_type"`
	} `json:"data"`
	Usage struct {
		Cost *float64 `json:"cost"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// Generate sends one /api/v1/images request per attempt, all at once: most
// of the models behind it take only n=1. A model's Sizes, when pinned, bound
// Request.Size; otherwise the size goes through for the API to judge. The
// size goes as the model's SizeForm says: the resolution tier, or explicit
// pixels (openrouterPixels). An
// answer holding more than one image keeps them all. Each request's
// usage.cost lands on its images or its Failure, so the round's costs sum to
// the bill. An answer without usage.cost keeps its images, marked
// "unreported": they were paid for.
func (o openrouter) Generate(ctx context.Context, r Request) ([]Image, []Failure, error) {
	if r.N <= 0 {
		return nil, nil, fmt.Errorf("openrouter: %d attempts asked for; need at least 1", r.N)
	}
	if len(r.Model.Sizes) > 0 && !slices.Contains(r.Model.Sizes, r.Size) {
		return nil, nil, fmt.Errorf("openrouter: %s has no size %q (sizes: %s)", r.Model.ID, r.Size, strings.Join(r.Model.Sizes, ", "))
	}
	key, err := r.Model.Key()
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{}
	for k, v := range r.Model.Params {
		body[k] = v
	}
	body["model"] = r.Model.ID
	body["prompt"] = r.Prompt
	if r.Model.SizeForm == "pixels" {
		size, err := openrouterPixels(r)
		if err != nil {
			return nil, nil, err
		}
		body["size"] = size
	} else {
		body["resolution"] = r.Size
		if r.Aspect != "" {
			body["aspect_ratio"] = r.Aspect
		}
	}
	if len(r.Refs) > 0 {
		refs := make([]openrouterRef, len(r.Refs))
		for i, ref := range r.Refs {
			refs[i].Type = "image_url"
			refs[i].ImageURL.URL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(ref.PNG)
		}
		body["input_references"] = refs
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("openrouter: %w", err)
	}
	url := baseURL("openrouter", openrouterDefaultURL) + "/api/v1/images"

	return fanOut(r.N, func(int) ([]Image, Failure, error) {
		return o.attempt(ctx, url, key, payload)
	})
}

// openrouterPixels is the explicit "WxH" a pixels-form model is sent: the
// tier's budget capped at the model's MaxPixels and MaxEdge (fitDims), at
// Request.Aspect, else the first reference's aspect, else square. It goes
// without aspect_ratio, which OpenRouter refuses when it disagrees with an
// explicit size, as the rounding can make it.
func openrouterPixels(r Request) (string, error) {
	aw, ah := 1.0, 1.0
	switch {
	case r.Aspect != "":
		var ok bool
		if aw, ah, ok = parseAspect(r.Aspect); !ok {
			return "", fmt.Errorf("openrouter: aspect %q is not W:H", r.Aspect)
		}
	case len(r.Refs) > 0:
		cfg, _, err := image.DecodeConfig(bytes.NewReader(r.Refs[0].PNG))
		if err != nil {
			return "", fmt.Errorf("openrouter: reference 1 is not a readable PNG: %w", err)
		}
		aw, ah = float64(cfg.Width), float64(cfg.Height)
	}
	px := min(tierPixels[r.Size], r.Model.MaxPixels)
	w, h := fitDims(px, aw, ah, r.Model.MaxEdge)
	if w < 64 || h < 64 {
		return "", fmt.Errorf("openrouter: aspect %g:%g is too extreme at size %s", aw, ah, r.Size)
	}
	return fmt.Sprintf("%dx%d", w, h), nil
}

// attempt makes one request. It returns every usable image in the answer,
// splitting usage.cost evenly over them, or why there is none (a Failure
// carrying any cost billed, for an answer without a usable image; an error
// for a failed call).
func (openrouter) attempt(ctx context.Context, url, key string, payload []byte) ([]Image, Failure, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, Failure{}, fmt.Errorf("openrouter: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	raw, err := doRaw(ctx, "openrouter", req)
	if err != nil {
		return nil, Failure{}, err
	}
	var resp openrouterResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, Failure{}, fmt.Errorf("openrouter: decoding the response: %w", err)
	}
	var billed float64
	if c := resp.Usage.Cost; c != nil {
		billed = *c
	}
	if len(resp.Data) == 0 {
		reason := "openrouter: no image returned"
		if len(resp.Error) > 0 && string(resp.Error) != "null" {
			reason += ": " + bodyMessage(raw, http.StatusOK)
		}
		return nil, Failure{Reason: reason, CostUSD: billed}, nil
	}
	var pngs [][]byte
	var why []string
	for _, d := range resp.Data {
		png, err := base64.StdEncoding.DecodeString(d.B64)
		switch {
		case err != nil:
			why = append(why, "image data is not base64: "+err.Error())
		case !bytes.HasPrefix(png, pngMagic):
			why = append(why, fmt.Sprintf("returned image is not a PNG (%s)", d.MediaType))
		default:
			pngs = append(pngs, png)
		}
	}
	if len(pngs) == 0 {
		return nil, Failure{Reason: "openrouter: " + strings.Join(why, "; "), CostUSD: billed}, nil
	}
	imgs := make([]Image, len(pngs))
	for i, png := range pngs {
		imgs[i] = Image{PNG: png, CostBasis: "unreported"}
		if resp.Usage.Cost != nil {
			imgs[i].CostUSD, imgs[i].CostBasis = billed/float64(len(pngs)), "reported"
		}
	}
	return imgs, Failure{}, nil
}
