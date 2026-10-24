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

type bflSubmit struct {
	ID string `json:"id"`
}

type bflPoll struct {
	Status string `json:"status"`
	Result struct {
		Sample string `json:"sample"`
	} `json:"result"`
}

// bflDims is the width and height for size and aspect (fitDims): the tier's
// pixels, capped at m.MaxPixels. The multiple of 16 is plate's own choice;
// BFL documents only a 64 pixel minimum.
func bflDims(m pins.ImageModel, size, aspect string) (int, int, error) {
	aw, ah := 1.0, 1.0
	if aspect != "" {
		var ok bool
		if aw, ah, ok = parseAspect(aspect); !ok {
			return 0, 0, fmt.Errorf("bfl: aspect %q is not W:H", aspect)
		}
	}
	wi, hi := fitDims(min(tierPixels[size], m.MaxPixels), aw, ah, 0)
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
// settles. Each job is billed on its own, and only a Ready one is billed, so
// only a failure that may have reached Ready carries a cost. The aspect and
// refs are read before any request.
func (b bfl) Generate(ctx context.Context, r Request) ([]Image, []Failure, error) {
	w, h, err := bflDims(r.Model, r.Size, r.Aspect)
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

	return fanOut(r.N, func(int) ([]Image, Failure, error) {
		return one(b.attempt(ctx, base, endpoint, key, payload, cost))
	})
}

// attempt runs one job: submit, poll to a settled status, fetch the sample.
// It returns the image, or why there is none (a Failure for a job that ended
// without one, an error for a failed call).
//
// The poll goes to the base host's documented /v1/get_result, never to the
// answer's polling_url, so the key only ever travels to the configured host.
// A job that ended without an image after it may have been billed (cancelled
// mid-poll, a poll that failed, Ready but unusable) carries the job's cost; a moderated or
// errored one does not, since BFL bills only Ready jobs.
func (bfl) attempt(ctx context.Context, base, endpoint, key string, payload []byte, cost float64) (*Image, Failure, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, Failure{}, fmt.Errorf("bfl: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-key", key)
	var job bflSubmit
	if err := do(ctx, "bfl", req, &job); err != nil {
		return nil, Failure{}, err
	}
	if job.ID == "" {
		return nil, Failure{Reason: "bfl: the job answer has no id"}, nil
	}
	gaveUp := func(last string) (*Image, Failure, error) {
		return nil, Failure{Reason: fmt.Sprintf("bfl: gave up waiting on job %s; last status %q", job.ID, last), CostUSD: cost}, nil
	}
	pollURL := base + "/v1/get_result?id=" + url.QueryEscape(job.ID)
	last := "not polled"
	notFound := 0
	for {
		preq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return nil, Failure{}, fmt.Errorf("bfl: %w", err)
		}
		preq.Header.Set("x-key", key)
		var p bflPoll
		if err := do(ctx, "bfl", preq, &p); err != nil {
			if ctx.Err() != nil {
				return gaveUp(last)
			}
			// The job was taken and may yet be billed.
			return nil, Failure{Reason: fmt.Sprintf("bfl: polling job %s: %v", job.ID, err), CostUSD: cost}, nil
		}
		last = p.Status
		switch p.Status {
		case "Ready":
			return fetchSample(ctx, job.ID, p.Result.Sample, cost)
		case "Pending", "Reasoning", "Generating":
		case "Task not found":
			// A fresh job can be unknown for a poll or two.
			if notFound++; notFound > bflNotFoundGrace {
				return nil, Failure{Reason: fmt.Sprintf("bfl: job %s ended with status %q", job.ID, p.Status)}, nil
			}
		default:
			// Request Moderated, Content Moderated, Error, ...
			return nil, Failure{Reason: fmt.Sprintf("bfl: job %s ended with status %q", job.ID, p.Status)}, nil
		}
		if err := sleep(ctx, bflPollEvery); err != nil {
			return gaveUp(last)
		}
	}
}

// fetchSample downloads the signed result URL, which is on another host and
// takes no key. The job is Ready, so every way this fails carries its cost.
func fetchSample(ctx context.Context, id, sample string, cost float64) (*Image, Failure, error) {
	if sample == "" {
		return nil, Failure{Reason: fmt.Sprintf("bfl: job %s is Ready but has no result.sample", id), CostUSD: cost}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sample, nil)
	if err != nil {
		return nil, Failure{}, fmt.Errorf("bfl: %w", err)
	}
	png, err := doRaw(ctx, "bfl", req)
	if err != nil {
		return nil, Failure{Reason: fmt.Sprintf("bfl: job %s is Ready but its image could not be fetched: %v", id, err), CostUSD: cost}, nil
	}
	if !bytes.HasPrefix(png, pngMagic) {
		return nil, Failure{Reason: fmt.Sprintf("bfl: job %s returned an image that is not a PNG", id), CostUSD: cost}, nil
	}
	return &Image{PNG: png, CostUSD: cost, CostBasis: "rate", ProviderID: id}, Failure{}, nil
}
