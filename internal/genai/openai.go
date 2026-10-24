package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/pins"
)

const openaiDefaultURL = "https://api.openai.com"

type openai struct{}

func init() { providers["openai"] = openai{} }

func (openai) Name() string { return "openai" }

// openaiSize is the "WxH" the model makes at aspect (its AspectPixels),
// square when no aspect is asked for.
func openaiSize(m pins.ImageModel, aspect string) (string, error) {
	if aspect == "" {
		aspect = "1:1"
	}
	if size, ok := m.AspectPixels[aspect]; ok {
		return size, nil
	}
	aspects := make([]string, 0, len(m.AspectPixels))
	for a := range m.AspectPixels {
		aspects = append(aspects, a)
	}
	sort.Strings(aspects)
	offered := make([]string, len(aspects))
	for i, a := range aspects {
		offered[i] = m.AspectPixels[a] + " (" + a + ")"
	}
	return "", fmt.Errorf("openai: %s cannot make aspect %q (sizes: %s)", m.ID, aspect, strings.Join(offered, ", "))
}

type openaiResponse struct {
	Data []struct {
		B64 string `json:"b64_json"`
	} `json:"data"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Generate sends one edit request with n=N. The body is built into a byte
// slice so http.NewRequest gives it a GetBody for retries. Any failed call is
// the round's error; only the first N entries of data are kept, and one missing
// or unusable becomes a Failure. The whole billed usage cost is split evenly
// over the images returned, so their CostUSD sums to the bill; a 200 with no
// usable image splits the bill over its Failures instead. ProviderID is empty
// because do does not expose response headers.
func (openai) Generate(ctx context.Context, r Request) ([]Image, []Failure, error) {
	size, err := openaiSize(r.Model, r.Aspect)
	if err != nil {
		return nil, nil, err
	}
	key, err := r.Model.Key()
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := [][2]string{{"model", r.Model.ID}, {"prompt", r.Prompt}, {"n", strconv.Itoa(r.N)},
		{"size", size}, {"quality", "high"}, {"output_format", "png"}}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return nil, nil, fmt.Errorf("openai: %w", err)
		}
	}
	for i, ref := range r.Refs {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image[]"; filename="ref%d.png"`, i+1))
		h.Set("Content-Type", "image/png")
		w, err := mw.CreatePart(h)
		if err == nil {
			_, err = w.Write(ref.PNG)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("openai: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, nil, fmt.Errorf("openai: %w", err)
	}
	body := buf.Bytes()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL("openai", openaiDefaultURL)+"/v1/images/edits", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("openai: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)

	var resp openaiResponse

	if err := do(ctx, "openai", req, &resp); err != nil {
		return nil, nil, err
	}
	billed := (float64(resp.Usage.InputTokens)*r.Model.InPerMTok + float64(resp.Usage.OutputTokens)*r.Model.OutPerMTok) / 1e6
	var pngs [][]byte
	var fails []Failure
	for i := 0; i < r.N; i++ {
		reason := ""
		if i >= len(resp.Data) {
			reason = "openai: no image returned for this attempt"
		} else if png, err := base64.StdEncoding.DecodeString(resp.Data[i].B64); err != nil {
			reason = "openai: image data is not base64: " + err.Error()
		} else if !bytes.HasPrefix(png, pngMagic) {
			reason = "openai: returned image is not a PNG"
		} else {
			pngs = append(pngs, png)
			continue
		}
		fails = append(fails, Failure{Index: i, Reason: reason})
	}
	if len(pngs) == 0 {
		for i := range fails {
			fails[i].CostUSD = billed / float64(len(fails))
		}
		return nil, fails, nil
	}
	imgs := make([]Image, len(pngs))
	for i, png := range pngs {
		imgs[i] = Image{PNG: png, CostUSD: billed / float64(len(pngs)), CostBasis: "usage"}
	}
	return imgs, fails, nil
}
