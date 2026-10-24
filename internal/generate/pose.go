package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A pose folder holds DIR/rounds/NN/aMM.png and aMM.json, NN and MM two
// digits from 01. A round is written as NN.partial and renamed to NN once
// its records are in, so a round folder without the suffix is complete.
const partialSuffix = ".partial"

func roundsDir(pose string) string { return filepath.Join(pose, "rounds") }

func roundName(n int) string { return fmt.Sprintf("%02d", n) }

func attemptBase(n int) string { return fmt.Sprintf("a%02d", n) }

// rounds lists the complete round numbers under pose, ascending. A missing
// rounds folder is no rounds.
func rounds(pose string) ([]int, error) {
	es, err := os.ReadDir(roundsDir(pose))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ns []int
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(e.Name()); err == nil && n > 0 {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	return ns, nil
}

// LoadPose reads the records of every complete round under pose, in round
// then attempt order. A partial round is not read.
func LoadPose(pose string) ([]Record, error) {
	ns, err := rounds(pose)
	if err != nil {
		return nil, err
	}
	var rs []Record
	for _, n := range ns {
		dir := filepath.Join(roundsDir(pose), roundName(n))
		es, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var round []Record
		for _, e := range es {
			if e.IsDir() || !attemptName.MatchString(e.Name()) || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			r, err := readRecord(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			round = append(round, r)
		}
		sort.SliceStable(round, func(i, j int) bool { return round[i].Attempt < round[j].Attempt })
		rs = append(rs, round...)
	}
	return rs, nil
}

// PoseTotal is the sum of the records' costs, in USD.
func PoseTotal(rs []Record) float64 {
	var t float64
	for _, r := range rs {
		t += r.CostUSD
	}
	return t
}

// newRound removes any partial round a failed run left, then creates the
// next round's partial folder and returns its number and path.
func newRound(pose string) (int, string, error) {
	es, err := os.ReadDir(roundsDir(pose))
	if err != nil && !os.IsNotExist(err) {
		return 0, "", err
	}
	for _, e := range es {
		if e.IsDir() && strings.HasSuffix(e.Name(), partialSuffix) {
			if err := os.RemoveAll(filepath.Join(roundsDir(pose), e.Name())); err != nil {
				return 0, "", err
			}
		}
	}
	ns, err := rounds(pose)
	if err != nil {
		return 0, "", err
	}
	n := 1
	if len(ns) > 0 {
		n = ns[len(ns)-1] + 1
	}
	if err := os.MkdirAll(roundsDir(pose), 0o755); err != nil {
		return 0, "", err
	}
	dir := filepath.Join(roundsDir(pose), roundName(n)+partialSuffix)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return 0, "", err
	}
	return n, dir, nil
}
