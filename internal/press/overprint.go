package press

import (
	"bytes"
	"errors"
	"regexp"
	"slices"
	"strconv"
)

// opState and koState are the graphics states withOverprint sets around a
// K-only text object: overprint on for it, and back to the PDF default
// after it, since BT and ET do not save the graphics state and the next
// fill would otherwise overprint too. OPM 1 leaves the zero C, M and Y of
// the text unpainted, so the colour under the text survives on those plates.
// Only the fill overprints: K-only is judged on the fill colour, so a
// stroke (text render modes 1 and 2) may be coloured and stays knocked out.
const (
	opState = "/PlateOP << /Type /ExtGState /OP false /op true /OPM 1 >>"
	koState = "/PlateKO << /Type /ExtGState /OP false /op false /OPM 0 >>"
)

// withOverprint sets every text object in qdf's page and form content that
// shows text only in a K-only fill (0 0 0 K k with K above 0, as withSpots
// writes black) to overprint, and returns how many it set. Black text
// knocked out of a colour leaves a hairline of paper wherever the plates
// misregister; overprinted, it prints over the colour instead. The two
// states go into every resource dictionary in qdf, a superset of the ones
// the changed streams use, inline so fix-qdf has no new object to place.
func withOverprint(qdf []byte) ([]byte, int, error) {
	content := contentStreams(qdf)
	var n int
	out := eachStream(qdf, func(num, dict, data []byte, image bool) []byte {
		if image || (!content[string(num)] && !formDict.Match(dict)) {
			return data
		}
		b, k := overprintText(data)
		n += k
		return b
	})
	if n == 0 {
		return qdf, 0, nil
	}
	out, err := withStates(out)
	return out, n, err
}

var (
	formDict    = regexp.MustCompile(`/Subtype\s*/Form\b`)
	contentsRef = regexp.MustCompile(`/Contents\s*(\d+) 0 R|/Contents\s*\[([^\]]*)\]`)
	objRef      = regexp.MustCompile(`(\d+) 0 R`)
)

// contentStreams is the object numbers of the streams a page's /Contents
// names.
func contentStreams(qdf []byte) map[string]bool {
	nums := map[string]bool{}
	for _, m := range contentsRef.FindAllSubmatch(qdf, -1) {
		if m[1] != nil {
			nums[string(m[1])] = true
		}
		for _, r := range objRef.FindAllSubmatch(m[2], -1) {
			nums[string(r[1])] = true
		}
	}
	return nums
}

// eachStream replaces the data of every stream in qdf with what f returns,
// giving it the stream's object number and dictionary and whether it is an
// image. Data after an unterminated stream is kept as it is.
func eachStream(qdf []byte, f func(num, dict, data []byte, image bool) []byte) []byte {
	var out []byte
	for {
		i := bytes.Index(qdf, []byte("\nstream\n"))
		if i < 0 {
			return append(out, qdf...)
		}
		head := i + len("\nstream\n")
		out = append(out, qdf[:head]...)
		dict := qdf[:i]
		var num []byte
		if o := bytes.LastIndex(dict, []byte(" obj\n")); o >= 0 {
			line := dict[bytes.LastIndexByte(dict[:o], '\n')+1 : o]
			num, _, _ = bytes.Cut(line, []byte(" "))
			dict = dict[o:]
		}
		end := bytes.Index(qdf[head:], []byte("\nendstream"))
		if end < 0 {
			return append(out, qdf[head:]...)
		}
		out = append(out, f(num, dict, qdf[head:head+end], imageDict.Match(dict))...)
		qdf = qdf[head+end:]
	}
}

// overprintText wraps each text object in content whose shown text is all
// in a K-only fill in /PlateOP gs and /PlateKO gs, and returns how many it
// wrapped. The fill is tracked through q and Q; any fill operator but a
// K-only k counts as colour.
func overprintText(s []byte) ([]byte, int) {
	type edit struct {
		at   int
		text string
	}
	var (
		edits      []edit
		ops        [][]byte
		fillK      bool
		saved      []bool
		inText     bool
		shown, all bool
		btEnd      int
	)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case isSpace(c):
			i++
			continue
		case c == '%':
			for i < len(s) && s[i] != '\n' && s[i] != '\r' {
				i++
			}
			continue
		case c == '(':
			i = skipString(s, i)
			ops = append(ops, nil)
			continue
		case c == '<' && i+1 < len(s) && s[i+1] == '<':
			i = skipDict(s, i+2)
			ops = append(ops, nil)
			continue
		case c == '<':
			for i < len(s) && s[i] != '>' {
				i++
			}
			i++
			ops = append(ops, nil)
			continue
		case c == '/':
			j := i + 1
			for j < len(s) && !isSpace(s[j]) && !isDelim(s[j]) {
				j++
			}
			ops = append(ops, s[i:j])
			i = j
			continue
		case isDelim(c):
			i++
			continue
		}
		j := i + 1
		for j < len(s) && !isSpace(s[j]) && !isDelim(s[j]) {
			j++
		}
		tok := s[i:j]
		i = j
		if c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9') {
			ops = append(ops, tok)
			continue
		}
		switch string(tok) {
		case "q":
			saved = append(saved, fillK)
		case "Q":
			if len(saved) > 0 {
				fillK = saved[len(saved)-1]
				saved = saved[:len(saved)-1]
			}
		case "k":
			fillK = kOnly(ops)
		case "g", "rg", "sc", "scn", "cs":
			fillK = false
		case "BT":
			inText, shown, all, btEnd = true, false, true, i
		case "Tj", "TJ", "'", `"`:
			shown, all = true, all && fillK
		case "ET":
			if inText && shown && all {
				edits = append(edits, edit{btEnd, "\n/PlateOP gs"}, edit{i, "\n/PlateKO gs"})
			}
			inText = false
		case "BI":
			i = skipInlineImage(s, i)
		}
		ops = ops[:0]
	}
	if len(edits) == 0 {
		return s, 0
	}
	var out []byte
	at := 0
	for _, e := range edits {
		out = append(append(out, s[at:e.at]...), e.text...)
		at = e.at
	}
	return append(out, s[at:]...), len(edits) / 2
}

// kOnly reports whether the operands of a k are 0 0 0 K with K above 0.
func kOnly(ops [][]byte) bool {
	if len(ops) < 4 {
		return false
	}
	var v [4]float64
	for i, o := range ops[len(ops)-4:] {
		f, err := strconv.ParseFloat(string(o), 64)
		if err != nil {
			return false
		}
		v[i] = f
	}
	return v[0] == 0 && v[1] == 0 && v[2] == 0 && v[3] > 0
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isDelim(c byte) bool { return bytes.IndexByte([]byte("()<>[]{}/%"), c) >= 0 }

// skipString returns the index after the literal string that opens at s[i].
func skipString(s []byte, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return i
}

// skipDict returns the index after the >> closing the dictionary whose <<
// ends just before s[i].
func skipDict(s []byte, i int) int {
	depth := 1
	for i < len(s) {
		switch {
		case s[i] == '(':
			i = skipString(s, i)
			continue
		case bytes.HasPrefix(s[i:], []byte("<<")):
			depth++
			i += 2
			continue
		case bytes.HasPrefix(s[i:], []byte(">>")):
			i += 2
			if depth--; depth == 0 {
				return i
			}
			continue
		}
		i++
	}
	return i
}

// inlineImageEnd is the EI that closes an inline image's data.
var inlineImageEnd = regexp.MustCompile(`\sEI(\s|$)`)

// skipInlineImage returns the index after the EI of the inline image whose
// BI ends just before s[i]: its data is binary, so no operator in it counts.
func skipInlineImage(s []byte, i int) int {
	id := bytes.Index(s[i:], []byte("ID"))
	if id < 0 {
		return len(s)
	}
	i += id + 3
	m := inlineImageEnd.FindIndex(s[i:])
	if m == nil {
		return len(s)
	}
	return i + m[1]
}

var (
	objHead  = regexp.MustCompile(`(?m)^(\d+) 0 obj\n`)
	resKey   = regexp.MustCompile(`/Resources\s*(?:(<<)|(\d+) 0 R)`)
	refValue = regexp.MustCompile(`^\s*(\d+) 0 R`)
)

// withStates adds opState and koState to the /ExtGState of every resource
// dictionary in qdf, inline or indirect, creating /ExtGState where it is
// absent and editing the object an indirect /ExtGState names.
func withStates(qdf []byte) ([]byte, error) {
	// dictAt is where each object's own dictionary opens, after its <<.
	dictAt := map[string]int{}
	type span struct{ from, to int }
	var heads []span
	for _, m := range objHead.FindAllSubmatchIndex(qdf, -1) {
		body := qdf[m[1]:]
		end := len(body)
		for _, stop := range []string{"\nstream\n", "\nendobj"} {
			if e := bytes.Index(body, []byte(stop)); e >= 0 && e < end {
				end = e
			}
		}
		heads = append(heads, span{m[1], m[1] + end})
		if bytes.HasPrefix(body, []byte("<<")) {
			dictAt[string(qdf[m[2]:m[3]])] = m[1] + 2
		}
	}
	at := map[int]bool{}
	for _, h := range heads {
		for _, m := range resKey.FindAllSubmatchIndex(qdf[h.from:h.to], -1) {
			res := h.from + m[1]
			if m[2] < 0 {
				var ok bool
				if res, ok = dictAt[string(qdf[h.from+m[4]:h.from+m[5]])]; !ok {
					return nil, errors.New("a /Resources reference names no dictionary")
				}
			}
			key := keyIn(qdf, res, "/ExtGState")
			switch {
			case key < 0:
				at[res] = false
			case bytes.HasPrefix(bytes.TrimLeft(qdf[key:], " \n\r\t"), []byte("<<")):
				at[key+bytes.Index(qdf[key:], []byte("<<"))+2] = true
			default:
				r := refValue.FindSubmatch(qdf[key:])
				if r == nil {
					return nil, errors.New("an /ExtGState is neither a dictionary nor a reference to one")
				}
				g, ok := dictAt[string(r[1])]
				if !ok {
					return nil, errors.New("an /ExtGState reference names no dictionary")
				}
				at[g] = true
			}
		}
	}
	if len(at) == 0 {
		return nil, errors.New("no resource dictionary to hold the overprint state")
	}
	offs := make([]int, 0, len(at))
	for o := range at {
		offs = append(offs, o)
	}
	slices.Sort(offs)
	var out []byte
	prev := 0
	for _, o := range offs {
		text := " " + opState + " " + koState
		if !at[o] {
			text = " /ExtGState <<" + text + " >>"
		}
		out = append(append(out, qdf[prev:o]...), text...)
		prev = o
	}
	return append(out, qdf[prev:]...), nil
}

// keyIn returns the index just after key among the entries of the
// dictionary whose << ends just before qdf[i], or -1 when it has none.
func keyIn(qdf []byte, i int, key string) int {
	depth := 1
	for i < len(qdf) {
		switch {
		case qdf[i] == '(':
			i = skipString(qdf, i)
			continue
		case bytes.HasPrefix(qdf[i:], []byte("<<")):
			depth++
			i += 2
			continue
		case bytes.HasPrefix(qdf[i:], []byte(">>")):
			if depth--; depth == 0 {
				return -1
			}
			i += 2
			continue
		case depth == 1 && bytes.HasPrefix(qdf[i:], []byte(key)):
			if e := i + len(key); e < len(qdf) && (isSpace(qdf[e]) || isDelim(qdf[e])) {
				return e
			}
		}
		i++
	}
	return -1
}
