package generate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lockyc/plate/internal/atomicfile"
	"github.com/lockyc/plate/internal/cli"
)

const acceptUsage = "generate accept --pose DIR <rNN-aMM> <out.png>"

// attemptID matches the id the contact sheet labels an attempt with, rNN-aMM.
var attemptID = regexp.MustCompile(`^(?i)r([0-9]{2,})-a([0-9]{2,})$`)

// acceptMain copies one attempt of a complete round to out.png and writes
// its Record, with every ref's chain embedded, to out.provenance.json
// beside it. It overwrites nothing: a person deletes an old copy on purpose.
func acceptMain(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := cli.Flags("generate accept", acceptUsage, stderr)
	pose := fs.String("pose", "", "the pose folder the attempt is in (required)")
	rest, code, ok := cli.Parse(fs, args, 2)
	if !ok {
		return code
	}
	id, out := rest[0], rest[1]
	if *pose == "" || strings.ToLower(filepath.Ext(out)) != ".png" {
		return cli.Usage(stderr, "generate accept", "give --pose, and an output ending .png")
	}
	prov := strings.TrimSuffix(out, filepath.Ext(out)) + ".provenance.json"
	for _, p := range []string{out, prov} {
		if _, err := os.Lstat(p); err == nil {
			return cli.Fail(stderr, "generate accept", fmt.Errorf("%s already exists; delete it first if you mean to replace it", p))
		} else if !os.IsNotExist(err) {
			return cli.Fail(stderr, "generate accept", err)
		}
	}
	src, rec, err := findAttempt(*pose, id)
	if err != nil {
		return cli.Fail(stderr, "generate accept", err)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return cli.Fail(stderr, "generate accept", err)
	}
	if err := atomicfile.WriteFile(out, b, 0o644); err != nil {
		return cli.Fail(stderr, "generate accept", err)
	}
	if err := writeRecord(prov, rec); err != nil {
		return cli.Fail(stderr, "generate accept", err)
	}
	fmt.Fprintln(stdout, out)
	fmt.Fprintln(stdout, prov)
	return 0
}

// findAttempt returns the image path and Record of attempt id (rNN-aMM) in a
// complete round of pose. An unknown id's error lists the attempts there are.
func findAttempt(pose, id string) (string, Record, error) {
	ns, err := rounds(pose)
	if err != nil {
		return "", Record{}, err
	}
	var have []string
	for _, n := range ns {
		dir := filepath.Join(roundsDir(pose), roundName(n))
		es, err := os.ReadDir(dir)
		if err != nil {
			return "", Record{}, err
		}
		for _, e := range es {
			if !e.IsDir() && attemptName.MatchString(e.Name()) && filepath.Ext(e.Name()) == ".png" {
				have = append(have, fmt.Sprintf("r%s-%s", roundName(n), strings.TrimSuffix(e.Name(), ".png")))
			}
		}
	}
	if m := attemptID.FindStringSubmatch(id); m != nil {
		r, _ := strconv.Atoi(m[1])
		a, _ := strconv.Atoi(m[2])
		dir := filepath.Join(roundsDir(pose), roundName(r))
		base := filepath.Join(dir, attemptBase(a))
		for _, n := range ns {
			if n != r {
				continue
			}
			if _, err := os.Stat(base + ".png"); err != nil {
				break
			}
			rec, err := readRecord(base + ".json")
			if err != nil {
				return "", Record{}, err
			}
			return base + ".png", rec, nil
		}
	}
	list := "none"
	if len(have) > 0 {
		list = strings.Join(have, ", ")
	}
	return "", Record{}, fmt.Errorf("no attempt %q in %s; it has %s", id, pose, list)
}
