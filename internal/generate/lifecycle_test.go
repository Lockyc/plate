package generate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lockyc/plate/internal/pins"
	"pgregory.net/rapid"
)

// outcome is what the fake OpenRouter answers one attempt with.
type outcome int

const (
	oImage        outcome = iota // a PNG with its cost
	oImageNoCost                 // a PNG without usage.cost
	oNoData                      // 200, no image, charged
	oNoDataNoCost                // 200, no image, no usage.cost
	oBadImage                    // 200, a non-PNG image, charged
	oKeyRefused                  // 401
	oModerated                   // 403, OpenRouter's moderation refusal
	oCutOff                      // 200 whose body the connection drops
	nOutcomes
)

func (o outcome) String() string {
	return [...]string{"image", "image-no-cost", "no-data", "no-data-no-cost", "bad-image", "401", "403", "cut-off"}[o]
}

// answer is one scripted attempt: its outcome, its cost in cents and, for an
// image, the PNG it returns.
type answer struct {
	o     outcome
	cents int
	png   []byte
}

// image reports whether the attempt comes back as an image.
func (a answer) image() bool { return a.o == oImage || a.o == oImageNoCost }

// charged reports whether the attempt, failing, was or may have been charged.
func (a answer) charged() bool {
	switch a.o {
	case oNoData, oBadImage:
		return a.cents > 0
	case oNoDataNoCost, oCutOff:
		return true
	}
	return false
}

// unreported reports whether the attempt's cost never came back.
func (a answer) unreported() bool {
	return a.o == oImageNoCost || a.o == oNoDataNoCost || a.o == oCutOff
}

// script is the fake provider: each request takes the next queued answer
// (requests arrive in any order, so which attempt index gets which answer is
// the client's business), and the first request of a round runs plant.
type script struct {
	mu    sync.Mutex
	queue []answer
	plant func()
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.plant != nil {
		s.plant()
		s.plant = nil
	}
	a := s.queue[0]
	s.queue = s.queue[1:]
	s.mu.Unlock()
	cost := float64(a.cents) / 100
	w.Header().Set("Content-Type", "application/json")
	switch a.o {
	case oImage:
		json.NewEncoder(w).Encode(imageAnswer(b64(a.png), cost))
	case oImageNoCost:
		json.NewEncoder(w).Encode(imageAnswer(b64(a.png), nil))
	case oNoData:
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "usage": map[string]any{"cost": cost}})
	case oNoDataNoCost:
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	case oBadImage:
		json.NewEncoder(w).Encode(imageAnswer(b64([]byte("GIF89a not a png")), cost))
	case oKeyRefused:
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	case oModerated:
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"flagged"}}`))
	case oCutOff:
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{\"data\":[{\"b64_json\":\"")
		buf.Flush()
		conn.Close()
	}
}

// roundModel is one round the model expects on disk.
type roundModel struct {
	partial  bool
	images   map[string]int // PNG bytes to cents
	failures []answer       // the failed attempts, when any was charged
}

// lifecycle is the model: what the pose folder must hold.
type lifecycle struct {
	t       *testing.T
	dir     string
	pose    string
	ref     string
	srv     *script
	rounds  map[int]*roundModel // rounds a run made, by number
	planted map[int]string      // stray folders planted, by number: "partial", "partial+file" or "complete"
	accepts int                 // outputs accepted so far
	nextPNG int
}

func (m *lifecycle) taken() int {
	top := 0
	for n := range m.rounds {
		top = max(top, n)
	}
	for n := range m.planted {
		top = max(top, n)
	}
	return top
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

var unreportedLine = regexp.MustCompile(`did not report the cost of ([0-9]+) attempt`)

func (m *lifecycle) round(t *rapid.T) {
	answers := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) answer {
		a := answer{o: outcome(rapid.IntRange(0, int(nOutcomes)-1).Draw(t, "outcome"))}
		if a.o == oImage || a.o == oNoData || a.o == oBadImage {
			a.cents = rapid.IntRange(0, 40).Draw(t, "cents")
		}
		return a
	}), 1, 4).Draw(t, "answers")
	var imgs, failed []answer
	charged, unreported := false, 0
	for i := range answers {
		if answers[i].image() {
			m.nextPNG++
			answers[i].png = pngOf(m.t, 2, 2, color.NRGBA{uint8(m.nextPNG), uint8(m.nextPNG >> 8), 7, 255})
			imgs = append(imgs, answers[i])
		} else {
			failed = append(failed, answers[i])
		}
		charged = charged || answers[i].charged()
		if answers[i].unreported() {
			unreported++
		}
	}
	allRefused := !slices.ContainsFunc(answers, func(a answer) bool { return a.o != oKeyRefused })
	kept := !allRefused && (len(imgs) > 0 || charged)
	num := m.taken() + 1
	partial := filepath.Join(roundsDir(m.pose), roundName(num)+partialSuffix)
	failAt := 0
	if kept && len(imgs) > 0 && rapid.Bool().Draw(t, "fail a write") {
		failAt = rapid.IntRange(1, len(imgs)).Draw(t, "failed attempt")
	}
	m.srv.mu.Lock()
	m.srv.queue = answers
	if failAt > 0 {
		m.srv.plant = func() { os.MkdirAll(filepath.Join(partial, attemptBase(failAt)+".png", "x"), 0o755) }
	}
	m.srv.mu.Unlock()

	code, stdout, stderr := gen(m.t, "--pose", m.pose, "--prompt", "p", "--ref", m.ref, "--provider", "openrouter", "--n", strconv.Itoa(len(answers)))

	clean := failAt == 0 && len(failed) == 0
	if (code == 0) != clean {
		t.Fatalf("answers %v, failed write %d: code %d\nstdout %s\nstderr %s", answers, failAt, code, stdout, stderr)
	}
	if allRefused {
		unreported = 0
	}
	got := 0
	if mm := unreportedLine.FindStringSubmatch(stderr); mm != nil {
		got, _ = strconv.Atoi(mm[1])
	}
	// Only images written with an unreported cost are counted, so the one
	// whose write failed may be missing from the count; which image that is
	// depends on the order the answers arrived in.
	unwritten := 0
	if failAt > 0 && slices.ContainsFunc(imgs, func(a answer) bool { return a.o == oImageNoCost }) {
		unwritten = 1
	}
	if got != unreported && got != unreported-unwritten {
		t.Fatalf("answers %v: stderr reports %d unreported, want %d:\n%s", answers, got, unreported, stderr)
	}
	if !kept {
		if _, err := os.Stat(partial); !os.IsNotExist(err) {
			t.Fatalf("answers %v: a round with nothing paid left %s (%v)", answers, partial, err)
		}
		return
	}
	rm := &roundModel{partial: failAt > 0, images: map[string]int{}}
	for _, a := range imgs {
		rm.images[string(a.png)] = a.cents
		if a.o == oImageNoCost {
			rm.images[string(a.png)] = 0
		}
	}
	if charged {
		rm.failures = failed
	}
	m.rounds[num] = rm
	if failAt == 0 && unreported > 0 && !strings.Contains(stdout, fmt.Sprintf("\nunreported %d\n", unreported)) {
		t.Fatalf("answers %v: stdout lacks unreported %d:\n%s", answers, unreported, stdout)
	}
	if failAt == 0 && !strings.Contains(stdout, fmt.Sprintf("pose total $%.2f\n", float64(m.chargedCents())/100)) {
		t.Fatalf("answers %v: stdout's pose total is not $%.2f:\n%s", answers, float64(m.chargedCents())/100, stdout)
	}
}

// plantStray puts a folder a run did not make at a free number: an empty
// partial (another run's claim), a partial holding a file, or an empty
// complete round.
func (m *lifecycle) plantStray(t *rapid.T) {
	n := rapid.IntRange(1, m.taken()+2).Draw(t, "number")
	if _, ok := m.rounds[n]; ok {
		t.Skip("number taken")
	}
	if _, ok := m.planted[n]; ok {
		t.Skip("number taken")
	}
	kind := rapid.SampledFrom([]string{"partial", "partial+file", "complete"}).Draw(t, "kind")
	name := roundName(n)
	if kind != "complete" {
		name += partialSuffix
	}
	dir := filepath.Join(roundsDir(m.pose), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.t.Fatal(err)
	}
	if kind == "partial+file" {
		os.WriteFile(filepath.Join(dir, "a01.png"), []byte("someone's image"), 0o644)
	}
	m.planted[n] = kind
}

// accept copies a random attempt of a complete round out, its image write
// failing at random.
func (m *lifecycle) accept(t *rapid.T) {
	var ids []string
	for n, rm := range m.rounds {
		if rm.partial {
			continue
		}
		for i := range len(rm.images) {
			ids = append(ids, fmt.Sprintf("r%02d-a%02d", n, i+1))
		}
	}
	if len(ids) == 0 {
		t.Skip("no attempt to accept")
	}
	sort.Strings(ids)
	id := rapid.SampledFrom(ids).Draw(t, "id")
	fail := rapid.Bool().Draw(t, "image write fails")
	if fail {
		prev := writeImage
		writeImage = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
		defer func() { writeImage = prev }()
	}
	m.accepts++
	out := filepath.Join(m.dir, "out", fmt.Sprintf("take%d.png", m.accepts))
	code, _, stderr := accept(m.t, "--pose", m.pose, id, out)
	if fail != (code != 0) {
		t.Fatalf("accept %s (write fails %v): code %d: %s", id, fail, code, stderr)
	}
	if !fail {
		var r, a int
		fmt.Sscanf(id, "r%02d-a%02d", &r, &a)
		src, _ := os.ReadFile(filepath.Join(roundsDir(m.pose), roundName(r), attemptBase(a)+".png"))
		if b, err := os.ReadFile(out); err != nil || !bytes.Equal(b, src) {
			t.Fatalf("accept %s: the copy is not the attempt (%v)", id, err)
		}
	}
}

func (m *lifecycle) chargedCents() int {
	c := 0
	for _, rm := range m.rounds {
		if rm.partial {
			continue
		}
		for _, cents := range rm.images {
			c += cents
		}
		for _, f := range rm.failures {
			if f.o == oNoData || f.o == oBadImage {
				c += f.cents
			}
		}
	}
	return c
}

// check holds after every action: the pose's spend is every charge the
// provider reported in a complete round; every image that came back is on
// disk, with its cost; every planted folder is still there; every accepted
// image has its record.
func (m *lifecycle) check(t *rapid.T) {
	recs, err := LoadPose(m.pose)
	if err != nil {
		t.Fatal(err)
	}
	fails, err := LoadFailures(m.pose)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := PoseTotal(recs)+ChargedTotal(fails), float64(m.chargedCents())/100; math.Abs(got-want) > 1e-9 {
		t.Fatalf("pose spend $%v, the provider charged $%v", got, want)
	}
	for n, rm := range m.rounds {
		name := roundName(n)
		if rm.partial {
			name += partialSuffix
		}
		dir := filepath.Join(roundsDir(m.pose), name)
		pngs, _ := filepath.Glob(filepath.Join(dir, "a*.png"))
		var onDisk []string
		for _, p := range pngs {
			if fi, err := os.Stat(p); err != nil || fi.IsDir() {
				continue
			}
			b, _ := os.ReadFile(p)
			cents, ok := rm.images[string(b)]
			if !ok {
				t.Fatalf("%s is no image the provider returned", p)
			}
			r, err := readRecord(strings.TrimSuffix(p, ".png") + ".json")
			if err != nil {
				t.Fatalf("%s has no record: %v", p, err)
			}
			if math.Abs(r.CostUSD-float64(cents)/100) > 1e-9 || r.Round != n {
				t.Fatalf("%s: record %+v, want round %d at %d cents", p, r, n, cents)
			}
			onDisk = append(onDisk, string(b))
		}
		want := len(rm.images)
		if rm.partial {
			want-- // the one whose write was made to fail
		}
		if len(onDisk) != want {
			t.Fatalf("round %s holds %d of the %d images that came back", name, len(onDisk), len(rm.images))
		}
		_, ferr := os.Stat(filepath.Join(dir, failuresName))
		if (len(rm.failures) > 0) != (ferr == nil) {
			t.Fatalf("round %s: failures.json present %v, want %v", name, ferr == nil, len(rm.failures) > 0)
		}
	}
	for n, kind := range m.planted {
		name := roundName(n)
		if kind != "complete" {
			name += partialSuffix
		}
		if _, err := os.Stat(filepath.Join(roundsDir(m.pose), name)); err != nil {
			t.Fatalf("planted %s (%s) is gone: %v", name, kind, err)
		}
	}
	outs, _ := filepath.Glob(filepath.Join(m.dir, "out", "*.png"))
	for _, o := range outs {
		if _, err := os.Stat(strings.TrimSuffix(o, ".png") + ".provenance.json"); err != nil {
			t.Fatalf("accepted %s has no record: %v", o, err)
		}
	}
}

// TestRoundLifecycleKeepsEveryPaidAttempt runs random sequences of rounds
// (each attempt answered by a random outcome, one image's write failing at
// random), stray round folders and accepts against one pose, and checks the
// pose against a model of what the provider returned and charged.
func TestRoundLifecycleKeepsEveryPaidAttempt(t *testing.T) {
	needMagick(t)
	m, err := pins.ImageModelFor("openrouter", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(m.KeyEnv, "test-key-not-real")
	s := &script{}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	t.Setenv("PLATE_GENAI_OPENROUTER_URL", srv.URL)
	ref := writeRef(t, t.TempDir(), "front.png")
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "out"), 0o755)
		lm := &lifecycle{t: t, dir: dir, pose: filepath.Join(dir, "wave"), ref: ref, srv: s,
			rounds: map[int]*roundModel{}, planted: map[int]string{}}
		rt.Repeat(map[string]func(*rapid.T){
			"round":  lm.round,
			"plant":  lm.plantStray,
			"accept": lm.accept,
			"":       lm.check,
		})
	})
}
