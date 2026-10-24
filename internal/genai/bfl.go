package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder for image.DecodeConfig
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lockyc/plate/internal/pins"
)

const bflDefaultURL = "https://api.bfl.ai"

// bflPollEvery is the wait between polls of one job.
const bflPollEvery = time.Second

// bflNotFoundGrace is how many "Task not found" polls a fresh job may answer
// before that status is final.
const bflNotFoundGrace = 2

type bfl struct{}

func init() { providers["bfl"] = bfl{} }

func (bfl) Name() string { return "bfl" }

// bflPixels is the pixel budget of each Request.Size: 1K is one megapixel and
// 2K four, the most BFL documents for an output. 4K is not offered.
var bflPixels = map[string]float64{"1K": 1e6, "2K": 4e6}

type bflSubmit struct {
	ID string `json:"id"`
}

type bflPoll struct {
	Status string `json:"status"`
	Result struct {
		Sample string `json:"sample"`
	} `json:"result"`
}

// bflDims is the width and height for size and aspect: the largest multiples
// of 16 whose area stays within the size's budget, so a job never bills
// beyond the megapixels it was priced at. The multiple of 16 is plate's own
// choice; BFL documents only a 64 pixel minimum.
func bflDims(size, aspect string) (int, int, error) {
	px, ok := bflPixels[size]
	if !ok {
		return 0, 0, fmt.Errorf("bfl: no size %q (sizes: 1K, 2K)", size)
	}
	aw, ah := 1.0, 1.0
	if aspect != "" {
		ws, hs, found := strings.Cut(aspect, ":")
		w, err1 := strconv.ParseFloat(ws, 64)
		h, err2 := strconv.ParseFloat(hs, 64)
		if !found || err1 != nil || err2 != nil || w <= 0 || h <= 0 {
			return 0, 0, fmt.Errorf("bfl: aspect %q is not W:H", aspect)
		}
		aw, ah = w, h
	}
	w := math.Sqrt(px * aw / ah)
	h := w * ah / aw
	wi, hi := int(w)/16*16, int(h)/16*16
	if wi < 64 || hi < 64 {
		return 0, 0, fmt.Errorf("bfl: aspect %q is too extreme at size %s", aspect, size)
	}
	return wi, hi, nil
}

// bflCost is one job's bill: the first output megapixel, each further one
// (the excess rounded up), and the refs' summed megapixels rounded up.
func bflCost(m pins.ImageModel, outMP, refMP float64) float64 {
	extra := math.Ceil(math.Max(0, outMP-1) - 1e-9)
	return m.FirstMP + m.ExtraMP*extra + m.RefMP*math.Ceil(refMP-1e-9)
}

// Generate submits one job per attempt, all at once, and polls each until it
// settles. Each job is billed on its own, and only a Ready one is billed, so a
// failure carries no cost. Everything is validated before any request.
func (b bfl) Generate(ctx context.Context, r Request) ([]Image, []Failure, error) {
	if r.N <= 0 {
		return nil, nil, fmt.Errorf("bfl: %d attempts asked for; need at least 1", r.N)
	}
	if len(r.Refs) > r.Model.MaxRefs {
		return nil, nil, fmt.Errorf("bfl: %s takes at most %d reference images; %d given", r.Model.ID, r.Model.MaxRefs, len(r.Refs))
	}
	w, h, err := bflDims(r.Size, r.Aspect)
	if err != nil {
		return nil, nil, err
	}
	var refMP float64
	body := map[string]any{"prompt": r.Prompt, "output_format": "png", "width": w, "height": h}
	for i, ref := range r.Refs {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(ref.PNG))
		if err != nil {
			return nil, nil, fmt.Errorf("bfl: reference %d is not a readable PNG: %w", i+1, err)
		}
		refMP += float64(cfg.Width*cfg.Height) / 1e6
		name := "input_image"
		if i > 0 {
			name += "_" + strconv.Itoa(i+1)
		}
		body[name] = base64.StdEncoding.EncodeToString(ref.PNG)
	}
	key, err := r.Model.Key()
	if err != nil {
		return nil, nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("bfl: %w", err)
	}
	cost := bflCost(r.Model, float64(w*h)/1e6, refMP)
	base := baseURL("bfl", bflDefaultURL)
	endpoint := base + "/v1/" + r.Model.ID

	type result struct {
		img  *Image
		fail string
		err  error
	}
	results := make([]result, r.N)
	var wg sync.WaitGroup
	for i := 0; i < r.N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			img, fail, err := b.attempt(ctx, base, endpoint, key, payload, cost)
			results[i] = result{img, fail, err}
		}(i)
	}
	wg.Wait()

	var imgs []Image
	var fails []Failure
	for i, res := range results {
		switch {
		case res.err != nil && isAuth(res.err):
			return nil, nil, res.err
		case res.err != nil:
			fails = append(fails, Failure{Index: i, Reason: res.err.Error()})
		case res.img == nil:
			fails = append(fails, Failure{Index: i, Reason: res.fail})
		default:
			imgs = append(imgs, *res.img)
		}
	}
	return imgs, fails, nil
}

// attempt runs one job: submit, poll to a settled status, fetch the sample.
// It returns the image, or why there is none (a reason for a job that ended
// without one, an error for a failed call).
//
// The poll goes to the base host's documented /v1/get_result, never to the
// answer's polling_url, so the key only ever travels to the configured host.
// A job that ended without an image after it may have been billed (cancelled
// mid-poll, Ready but unusable) says so in its reason; a moderated or errored
// one does not, since BFL bills only Ready jobs.
func (bfl) attempt(ctx context.Context, base, endpoint, key string, payload []byte, cost float64) (*Image, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("bfl: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-key", key)
	var job bflSubmit
	if err := do(ctx, "bfl", req, &job); err != nil {
		return nil, "", err
	}
	if job.ID == "" {
		return nil, "bfl: the job answer has no id", nil
	}
	billed := fmt.Sprintf("; BFL billed about $%.2f", cost)
	gaveUp := func(last string) (*Image, string, error) {
		return nil, fmt.Sprintf("bfl: gave up waiting on job %s; last status %q%s", job.ID, last, billed), nil
	}
	pollURL := base + "/v1/get_result?id=" + url.QueryEscape(job.ID)
	last := "not polled"
	notFound := 0
	for {
		preq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("bfl: %w", err)
		}
		preq.Header.Set("x-key", key)
		var p bflPoll
		if err := do(ctx, "bfl", preq, &p); err != nil {
			if ctx.Err() != nil {
				return gaveUp(last)
			}
			return nil, "", err
		}
		last = p.Status
		switch p.Status {
		case "Ready":
			return fetchSample(ctx, job.ID, p.Result.Sample, cost, billed)
		case "Pending", "Reasoning", "Generating":
		case "Task not found":
			// A fresh job can be unknown for a poll or two.
			if notFound++; notFound > bflNotFoundGrace {
				return nil, fmt.Sprintf("bfl: job %s ended with status %q", job.ID, p.Status), nil
			}
		default:
			// Request Moderated, Content Moderated, Error, ...
			return nil, fmt.Sprintf("bfl: job %s ended with status %q", job.ID, p.Status), nil
		}
		if err := sleep(ctx, bflPollEvery); err != nil {
			return gaveUp(last)
		}
	}
}

// fetchSample downloads the signed result URL, which is on another host and
// takes no key. The job is Ready, so every way this fails carries the billing
// note.
func fetchSample(ctx context.Context, id, sample string, cost float64, billed string) (*Image, string, error) {
	if sample == "" {
		return nil, fmt.Sprintf("bfl: job %s is Ready but has no result.sample%s", id, billed), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sample, nil)
	if err != nil {
		return nil, "", fmt.Errorf("bfl: %w", err)
	}
	png, err := doRaw(ctx, "bfl", req)
	if err != nil {
		return nil, fmt.Sprintf("bfl: job %s is Ready but its image could not be fetched: %v%s", id, err, billed), nil
	}
	if !bytes.HasPrefix(png, pngMagic) {
		return nil, fmt.Sprintf("bfl: job %s returned an image that is not a PNG%s", id, billed), nil
	}
	return &Image{PNG: png, CostUSD: cost, CostBasis: "rate", ProviderID: id}, "", nil
}
