package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lockyc/plate/internal/atomicfile"
)

// Record is the provenance of one attempt, written as aMM.json beside its
// aMM.png. It says where the image came from, not how to make it again.
type Record struct {
	Provider   string      `json:"provider"`
	Model      string      `json:"model"`
	Prompt     string      `json:"prompt"`
	Size       string      `json:"size"`
	Aspect     string      `json:"aspect"`
	Refs       []RefRecord `json:"refs"`
	Round      int         `json:"round"`
	Attempt    int         `json:"attempt"`
	CostUSD    float64     `json:"cost_usd"`
	CostBasis  string      `json:"cost_basis"`
	ProviderID string      `json:"provider_id"`
	Created    string      `json:"created"` // RFC 3339, UTC
}

// RefRecord is one reference an attempt was made from. Path is as it was
// given on the command line, a label only; SHA256 is the ref's identity, of
// the file as read before any normalisation. Attempt is set when the ref was itself an attempt (in any
// pose folder), and carries that attempt's own chain.
type RefRecord struct {
	Path    string  `json:"path"`
	SHA256  string  `json:"sha256"`
	Attempt *Record `json:"attempt,omitempty"`
}

// attemptName is an attempt's image or record name: aMM.png, aMM.json.
var attemptName = regexp.MustCompile(`^a[0-9]{2,}\.(png|json)$`)

// refRecord hashes the ref at path and, when an aMM.json sits beside an
// aMM.png, embeds that attempt's record.
func refRecord(path string) (RefRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return RefRecord{}, err
	}
	h := sha256.Sum256(b)
	rr := RefRecord{Path: path, SHA256: hex.EncodeToString(h[:])}
	if !attemptName.MatchString(filepath.Base(path)) || filepath.Ext(path) != ".png" {
		return rr, nil
	}
	side := strings.TrimSuffix(path, ".png") + ".json"
	if _, err := os.Stat(side); os.IsNotExist(err) {
		return rr, nil
	}
	r, err := readRecord(side)
	if err != nil {
		return RefRecord{}, err
	}
	rr.Attempt = &r
	return rr, nil
}

func readRecord(path string) (Record, error) {
	var r Record
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

func writeRecord(path string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(b, '\n'), 0o644)
}
