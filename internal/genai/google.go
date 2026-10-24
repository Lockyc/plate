package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const googleDefaultURL = "https://generativelanguage.googleapis.com"

// pngMagic opens every PNG file.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

type google struct{}

func init() { providers["google"] = google{} }

func (google) Name() string { return "google" }

type googleBlob struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type googlePart struct {
	InlineData *googleBlob `json:"inline_data,omitempty"`
	Text       string      `json:"text,omitempty"`
}

type googleRequest struct {
	Contents []struct {
		Role  string       `json:"role"`
		Parts []googlePart `json:"parts"`
	} `json:"contents"`
	GenerationConfig struct {
		ResponseModalities []string `json:"responseModalities"`
		ImageConfig        struct {
			AspectRatio string `json:"aspectRatio,omitempty"`
			ImageSize   string `json:"imageSize,omitempty"`
		} `json:"imageConfig"`
	} `json:"generationConfig"`
}

type googleResponse struct {
	ResponseID string `json:"responseId"`
	Candidates []struct {
		FinishReason  string `json:"finishReason"`
		FinishMessage string `json:"finishMessage"`
		Content       struct {
			Parts []struct {
				Text       string `json:"text"`
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

// Generate sends one generateContent request per attempt, all at once. The
// refs go first and the prompt last, the order Google's editing examples use.
func (g google) Generate(ctx context.Context, r Request) ([]Image, []Failure, error) {
	rate := r.Model.PerImage[r.Size]
	key, err := r.Model.Key()
	if err != nil {
		return nil, nil, err
	}
	var body googleRequest
	body.Contents = make([]struct {
		Role  string       `json:"role"`
		Parts []googlePart `json:"parts"`
	}, 1)
	body.Contents[0].Role = "user"
	for _, ref := range r.Refs {
		body.Contents[0].Parts = append(body.Contents[0].Parts, googlePart{InlineData: &googleBlob{
			MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(ref.PNG),
		}})
	}
	body.Contents[0].Parts = append(body.Contents[0].Parts, googlePart{Text: r.Prompt})
	body.GenerationConfig.ResponseModalities = []string{"IMAGE"}
	body.GenerationConfig.ImageConfig.ImageSize = r.Size
	body.GenerationConfig.ImageConfig.AspectRatio = r.Aspect
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("google: %w", err)
	}
	url := baseURL("google", googleDefaultURL) + "/v1beta/models/" + r.Model.ID + ":generateContent"

	return fanOut(r.N, func(int) ([]Image, Failure, error) {
		img, fail, err := g.attempt(ctx, url, key, payload, rate)
		return one(img, Failure{Reason: fail}, err)
	})
}

// attempt makes one request. It returns the image, or why there is none (a
// fail reason for an answer without an image, an error for a failed call).
func (google) attempt(ctx context.Context, url, key string, payload []byte, rate float64) (*Image, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("google: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	var resp googleResponse
	if err := do(ctx, "google", req, &resp); err != nil {
		return nil, "", err
	}
	var why []string
	if br := resp.PromptFeedback.BlockReason; br != "" {
		why = append(why, "prompt blocked: "+br)
	}
	for _, c := range resp.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData == nil {
				if t := strings.TrimSpace(p.Text); t != "" {
					why = append(why, "model said: "+t)
				}
				continue
			}
			png, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil {
				return nil, "google: image data is not base64: " + err.Error(), nil
			}
			if !bytes.HasPrefix(png, pngMagic) {
				return nil, fmt.Sprintf("google: returned image is not a PNG (%s)", p.InlineData.MimeType), nil
			}
			return &Image{PNG: png, CostUSD: rate, CostBasis: "rate", ProviderID: resp.ResponseID}, "", nil
		}
		if c.FinishReason != "" {
			reason := "finishReason " + c.FinishReason
			if c.FinishMessage != "" {
				reason += ": " + c.FinishMessage
			}
			why = append(why, reason)
		}
	}
	if len(why) == 0 {
		why = append(why, "no candidates")
	}
	return nil, "google: no image (" + strings.Join(why, "; ") + ")", nil
}
