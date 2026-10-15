package pdf

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/lockyc/plate/internal/atomicfile"
)

// dateRe matches an Info dictionary date's timestamp digits, kept as group 2
// so a rewrite never changes the file's length and its xref offsets hold.
var dateRe = regexp.MustCompile(`(/(?:CreationDate|ModDate)\s*\(D:)(\d{14})`)

// SourceDate is the time a reproducible artefact is stamped with:
// SOURCE_DATE_EPOCH when set (reproducible-builds.org), else the Unix epoch,
// so the same page renders to the same bytes on every run.
func SourceDate() (time.Time, error) {
	s, ok := os.LookupEnv("SOURCE_DATE_EPOCH")
	if !ok || s == "" {
		return time.Unix(0, 0).UTC(), nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH=%q is not a non-negative integer", s)
	}
	return time.Unix(n, 0).UTC(), nil
}

// PinDates rewrites path's CreationDate and ModDate timestamps to t, in UTC,
// in place. Chrome stamps both with the wall clock, which alone makes two
// renders of an unchanged page differ.
func PinDates(path string, t time.Time) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	stamp := []byte("${1}" + t.UTC().Format("20060102150405"))
	out := dateRe.ReplaceAll(data, stamp)
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, out, fi.Mode().Perm())
}
