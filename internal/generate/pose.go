package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// A pose folder holds DIR/rounds/NN/aMM.png and aMM.json, NN and MM two
// digits from 01. A round is written as NN.partial and renamed to NN once
// its records are in, so a round folder without the suffix is complete. A
// partial round a failed run left is never deleted: it may hold images the
// person paid for. It is not read as part of the pose, and no later round
// takes its number.
const partialSuffix = ".partial"

// roundDir matches a round folder's name: NN, or NN.partial.
var roundDir = regexp.MustCompile(`^([0-9]{2,})(\.partial)?$`)

func roundsDir(pose string) string { return filepath.Join(pose, "rounds") }

func roundName(n int) string { return fmt.Sprintf("%02d", n) }

func attemptBase(n int) string { return fmt.Sprintf("a%02d", n) }

// scan lists the complete and the partial round numbers under pose,
// ascending. A missing rounds folder is no rounds.
func scan(pose string) (complete, partial []int, err error) {
	es, err := os.ReadDir(roundsDir(pose))
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range es {
		m := roundDir.FindStringSubmatch(e.Name())
		if !e.IsDir() || m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			continue
		}
		if m[2] == "" {
			complete = append(complete, n)
		} else {
			partial = append(partial, n)
		}
	}
	sort.Ints(complete)
	sort.Ints(partial)
	return complete, partial, nil
}

// rounds lists the complete round numbers under pose, ascending.
func rounds(pose string) ([]int, error) {
	ns, _, err := scan(pose)
	return ns, err
}

// partialRounds names each partial round under pose relative to it, as
// rounds/NN.partial.
func partialRounds(pose string) ([]string, error) {
	_, ps, err := scan(pose)
	var names []string
	for _, n := range ps {
		names = append(names, filepath.Join("rounds", roundName(n)+partialSuffix))
	}
	return names, err
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

// newRound creates the next round's partial folder, numbered above every
// complete and every partial round, and returns its number and path.
func newRound(pose string) (int, string, error) {
	complete, partial, err := scan(pose)
	if err != nil {
		return 0, "", err
	}
	n := 1
	for _, ns := range [][]int{complete, partial} {
		if len(ns) > 0 && ns[len(ns)-1] >= n {
			n = ns[len(ns)-1] + 1
		}
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
