package raster

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// PAM is a CMYK image of 8-bit samples, interleaved C, M, Y, K per pixel.
type PAM struct {
	W, H int
	Pix  []byte
}

// ReadPAM reads a P7 file as Ghostscript's pam device writes it: WIDTH,
// HEIGHT, DEPTH 4, MAXVAL 255 and TUPLTYPE CMYK, then ENDHDR. Comment
// lines are skipped; any other header line, or a short body, is refused.
func ReadPAM(path string) (PAM, error) {
	f, err := os.Open(path)
	if err != nil {
		return PAM{}, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	bad := func(format string, a ...any) (PAM, error) {
		return PAM{}, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, a...))
	}
	line, err := r.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "P7" {
		return bad("not a P7 file")
	}
	var img PAM
	var depth, maxval int
	var tuple string
	for {
		line, err = r.ReadString('\n')
		if err != nil {
			return bad("header ends before ENDHDR")
		}
		line = strings.TrimSpace(line)
		if line == "ENDHDR" {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "WIDTH", "HEIGHT", "DEPTH", "MAXVAL":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return bad("%s %q is not a positive number", key, val)
			}
			switch key {
			case "WIDTH":
				img.W = n
			case "HEIGHT":
				img.H = n
			case "DEPTH":
				depth = n
			case "MAXVAL":
				maxval = n
			}
		case "TUPLTYPE":
			tuple = val
		default:
			return bad("unexpected header line %q", line)
		}
	}
	if tuple != "CMYK" || depth != 4 || maxval != 255 || img.W == 0 || img.H == 0 {
		return bad("want a CMYK PAM with DEPTH 4 and MAXVAL 255, got TUPLTYPE %q DEPTH %d MAXVAL %d", tuple, depth, maxval)
	}
	img.Pix = make([]byte, img.W*img.H*4)
	if _, err := io.ReadFull(r, img.Pix); err != nil {
		return bad("pixel data is short of %dx%d", img.W, img.H)
	}
	return img, nil
}
