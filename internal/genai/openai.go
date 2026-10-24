package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

const openaiDefaultURL = "https://api.openai.com"

type openai struct{}

func init() { providers["openai"] = openai{} }

func (openai) Name() string { return "openai" }

// openaiSizes maps a Request.Aspect to a size the edit endpoint documents as
// recommended. "1K" and "2K" both use it; 4K sizes are documented as
// experimental above 2560x1440, so they are not offered.
var openaiSizes = map[string]string{
	"": "1024x1024", "1:1": "1024x1024", "3:2": "1536x1024", "2:3": "1024x1536",
}

const openaiSizeList = "1024x1024 (1:1), 1536x1024 (3:2), 1024x1536 (2:3)"

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
// usable image is a round error stating the amount billed. ProviderID is empty
// because do does not expose response headers.
func (openai) Generate(ctx context.Context, r Request) ([]Image, []Failure, error) {
	if r.N <= 0 {
		return nil, nil, fmt.Errorf("openai: %d attempts asked for; need at least 1", r.N)
	}
	if r.Size != "1K" && r.Size != "2K" {
		return nil, nil, fmt.Errorf("openai: %s has no size %q (sizes: 1K, 2K)", r.Model.ID, r.Size)
	}
	size, ok := openaiSizes[r.Aspect]
	if !ok {
		return nil, nil, fmt.Errorf("openai: %s cannot make aspect %q (sizes: %s)", r.Model.ID, r.Aspect, openaiSizeList)
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
	var reasons []string
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
		reasons = append(reasons, reason)
	}
	if len(pngs) == 0 {
		return nil, nil, fmt.Errorf("openai: billed $%.2f and returned no usable image: %s", billed, strings.Join(reasons, "; "))
	}
	imgs := make([]Image, len(pngs))
	for i, png := range pngs {
		imgs[i] = Image{PNG: png, CostUSD: billed / float64(len(pngs)), CostBasis: "usage"}
	}
	return imgs, fails, nil
}
