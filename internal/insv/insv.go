// Package insv reads the trailer an Insta360 camera appends to a clip's
// .insv file: the camera, the frame, each lens's calibration and the IMU
// samples. Stitching and levelling a dual-fisheye clip need all of them, and
// the trailer is the only place the camera states them.
//
// The layout follows gyroflow's telemetry-parser (src/insta360/, MIT OR
// Apache-2.0, https://github.com/AdrianEddy/telemetry-parser). A trailer ends
// with a 72-byte footer: 32 bytes of padding, the trailer's whole size (u32),
// a version (u32) and the ASCII magic. Before the footer, records run back to
// back, each closed by a six-byte descriptor (format u8, id u8, size u32,
// little-endian, so the first two bytes read as one u16 give 0x101 for the
// metadata and 0x300 for the IMU), and the walk goes backwards from the
// footer. Record 1 (format 1) is the ExtraMetadata protobuf; record 3 is the
// IMU, one sample per entry: a u64 timestamp, then the accelerometer and the
// gyroscope, three axes each, as f64 (g, rad/s, millisecond timestamps), or,
// when ExtraMetadata's is_raw_gyro is set (the X3), as u16 counts offset by
// 32768 against the ranges in gyro_cfg_info (g, °/s, microsecond timestamps).
//
// Of a two-file clip only the front file (VID_*_00_*) carries the trailer.
package insv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Lens is one lens's calibration in pixels of the clip's own frame (its
// centre and image-circle radius) and its orientation in radians, as the
// camera states it: the back-to-back half turn between the two lenses is not
// in it (an X3 states both lenses at yaw ≈ 0).
type Lens struct{ CX, CY, R, Yaw, Pitch, Roll float64 }

// IMU is one sample: T in seconds from the first video frame (negative before
// it), Gyro in rad/s, Accel in m/s², both on the sensor's own axes.
type IMU struct {
	T           float64
	Gyro, Accel [3]float64
}

// Meta is what a clip's trailer states.
type Meta struct {
	Model, Serial string
	Width, Height int // one lens's frame
	FPS           float64
	Lenses        [2]Lens
	IMU           []IMU // time-ordered, T from the first video frame
}

// Magic closes every Insta360 trailer.
const Magic = "8db42d694ccc418790edff439fe026bf"

// FooterSize is the footer's length: padding, size, version, Magic.
const FooterSize = 32 + 4 + 4 + len(Magic)

// DescriptorSize is the length of the descriptor that closes each record.
const DescriptorSize = 1 + 1 + 4

// Record ids and formats (telemetry-parser's RecordType and RecordFormat).
const (
	RecordMetadata = 1
	RecordIMU      = 3
	FormatBinary   = 0
	FormatProtobuf = 1
)

// ExtraMetadata field numbers read here.
const (
	FieldSerial           = 1
	FieldCameraType       = 2
	FieldOffset           = 5
	FieldDimension        = 19 // Vector2{x: 1, y: 2}
	FieldFrameRate        = 20
	FieldFirstFrameTime   = 24
	FieldGyroTimestamp    = 28 // ms, double
	FieldHasGyroTimestamp = 29
	FieldIsRawGyro        = 62
	FieldGyroConfig       = 65 // GyroConfigInfo{acc_range: 1, gyro_range: 2}
)

// StandardGravity converts the IMU's g to m/s².
const StandardGravity = 9.80665

// Read returns the trailer of the .insv file at path.
func Read(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Meta{}, err
	}
	size := st.Size()
	recs, err := records(f, size)
	if err != nil {
		return Meta{}, fmt.Errorf("%s: %w", path, err)
	}
	meta, ok := recs[RecordMetadata]
	if !ok {
		return Meta{}, fmt.Errorf("%s: Insta360 trailer has no metadata record", path)
	}
	buf := make([]byte, meta.size)
	if _, err := f.ReadAt(buf, meta.at); err != nil {
		return Meta{}, fmt.Errorf("%s: %w", path, err)
	}
	x, err := decodeExtra(buf)
	if err != nil {
		return Meta{}, fmt.Errorf("%s: metadata: %w", path, err)
	}
	m := Meta{Model: x.cameraType, Serial: x.serial, Width: x.width, Height: x.height, FPS: float64(x.frameRate)}
	if m.Lenses, err = parseOffset(x.offset, m.Width, m.Height); err != nil {
		return Meta{}, fmt.Errorf("%s: %w", path, err)
	}
	if imu, ok := recs[RecordIMU]; ok {
		buf := make([]byte, imu.size)
		if _, err := f.ReadAt(buf, imu.at); err != nil {
			return Meta{}, fmt.Errorf("%s: %w", path, err)
		}
		if m.IMU, err = decodeIMU(buf, x); err != nil {
			return Meta{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return m, nil
}

// record is where one record's data sits in the file.
type record struct {
	at, size int64
}

// records walks the trailer back from its footer and returns each record by
// id; a later id (nearer the file's start) never overwrites an earlier one.
func records(r io.ReaderAt, size int64) (map[byte]record, error) {
	if size < int64(FooterSize) {
		return nil, errors.New("no Insta360 trailer")
	}
	foot := make([]byte, FooterSize)
	if _, err := r.ReadAt(foot, size-int64(FooterSize)); err != nil {
		return nil, err
	}
	if string(foot[FooterSize-len(Magic):]) != Magic {
		return nil, errors.New("no Insta360 trailer")
	}
	trailer := int64(binary.LittleEndian.Uint32(foot[32:]))
	if trailer < int64(FooterSize) || trailer > size {
		return nil, fmt.Errorf("Insta360 footer states a %d-byte trailer in a %d-byte file", trailer, size)
	}
	start := size - trailer
	out := map[byte]record{}
	desc := make([]byte, DescriptorSize)
	// end is where the next record's descriptor ends, walking backwards.
	for end := size - int64(FooterSize); end > start; {
		if end-int64(DescriptorSize) < start {
			return nil, fmt.Errorf("Insta360 trailer has %d stray bytes before its first record", end-start)
		}
		if _, err := r.ReadAt(desc, end-int64(DescriptorSize)); err != nil {
			return nil, err
		}
		id := desc[1]
		n := int64(binary.LittleEndian.Uint32(desc[2:]))
		at := end - int64(DescriptorSize) - n
		if at < start {
			return nil, fmt.Errorf("Insta360 record %d states %d bytes, running past the trailer's start", id, n)
		}
		if _, seen := out[id]; !seen {
			out[id] = record{at: at, size: n}
		}
		end = at
	}
	return out, nil
}

// extra is the part of ExtraMetadata Read uses.
type extra struct {
	serial, cameraType, offset string
	width, height              int
	frameRate                  int64
	firstFrame                 int64
	gyroTimestamp              float64
	hasGyroTimestamp, rawGyro  bool
	accRange, gyroRange        float64
}

func decodeExtra(b []byte) (extra, error) {
	x := extra{accRange: 16, gyroRange: 2000} // telemetry-parser's defaults
	err := walkProto(b, func(field int, v uint64, data []byte) error {
		switch field {
		case FieldSerial:
			x.serial = string(data)
		case FieldCameraType:
			x.cameraType = string(data)
		case FieldOffset:
			x.offset = string(data)
		case FieldDimension:
			return walkProto(data, func(f int, v uint64, _ []byte) error {
				switch f {
				case 1:
					x.width = int(int32(v))
				case 2:
					x.height = int(int32(v))
				}
				return nil
			})
		case FieldFrameRate:
			x.frameRate = int64(int32(v))
		case FieldFirstFrameTime:
			x.firstFrame = int64(v)
		case FieldGyroTimestamp:
			x.gyroTimestamp = math.Float64frombits(v)
		case FieldHasGyroTimestamp:
			x.hasGyroTimestamp = v != 0
		case FieldIsRawGyro:
			x.rawGyro = v != 0
		case FieldGyroConfig:
			return walkProto(data, func(f int, v uint64, _ []byte) error {
				switch {
				case f == 1 && v > 0:
					x.accRange = float64(v)
				case f == 2 && v > 0:
					x.gyroRange = float64(v)
				}
				return nil
			})
		}
		return nil
	})
	return x, err
}

// walkProto calls fn for each field of a protobuf message: v holds a varint
// or the bits of a fixed64/fixed32, data a length-delimited field's bytes.
func walkProto(b []byte, fn func(field int, v uint64, data []byte) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return errors.New("bad protobuf key")
		}
		b = b[n:]
		field := int(key >> 3)
		var v uint64
		var data []byte
		switch key & 7 {
		case 0:
			v, n = binary.Uvarint(b)
			if n <= 0 {
				return fmt.Errorf("protobuf field %d: bad varint", field)
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return fmt.Errorf("protobuf field %d: short fixed64", field)
			}
			v, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return fmt.Errorf("protobuf field %d: bad length", field)
			}
			data, b = b[n:n+int(l)], b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return fmt.Errorf("protobuf field %d: short fixed32", field)
			}
			v, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return fmt.Errorf("protobuf field %d: wire type %d", field, key&7)
		}
		if err := fn(field, v, data); err != nil {
			return err
		}
	}
	return nil
}

// parseOffset reads the `offset` calibration string: the lens count, then
// per lens its radius, centre x and y, yaw, pitch and roll (degrees), then
// the calibration frame's width and height (the lenses side by side) and a
// flags word. Lengths come back in pixels of one lens's w×h frame.
func parseOffset(s string, w, h int) ([2]Lens, error) {
	var out [2]Lens
	tok := strings.Split(strings.TrimSpace(s), "_")
	const per = 6
	if len(tok) != 1+2*per+3 || tok[0] != "2" {
		return out, fmt.Errorf("lens calibration %q is not a two-lens offset string", s)
	}
	v := make([]float64, len(tok)-1)
	for i, t := range tok[1:] {
		f, err := strconv.ParseFloat(t, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out, fmt.Errorf("lens calibration %q: field %d is not a number", s, i+1)
		}
		v[i] = f
	}
	calW, calH := v[2*per]/2, v[2*per+1]
	if calW <= 0 || calH <= 0 || w <= 0 || h <= 0 {
		return out, fmt.Errorf("lens calibration %q against a %dx%d frame: no frame to scale onto", s, w, h)
	}
	sx, sy := float64(w)/calW, float64(h)/calH
	for i := range out {
		p := v[i*per:]
		out[i] = Lens{
			R:     p[0] * sx,
			CX:    (p[1] - float64(i)*calW) * sx,
			CY:    p[2] * sy,
			Yaw:   p[3] * math.Pi / 180,
			Pitch: p[4] * math.Pi / 180,
			Roll:  p[5] * math.Pi / 180,
		}
	}
	return out, nil
}

// decodeIMU reads record 3's samples in x's format.
func decodeIMU(b []byte, x extra) ([]IMU, error) {
	entry, perSecond := 8+6*8, 1e3
	if x.rawGyro {
		entry, perSecond = 8+6*2, 1e6
	}
	if len(b)%entry != 0 {
		return nil, fmt.Errorf("IMU record of %d bytes is not whole %d-byte samples", len(b), entry)
	}
	shift := 0.0
	if x.hasGyroTimestamp {
		shift = x.gyroTimestamp / 1e3
	}
	accScale, gyroScale := StandardGravity, 1.0
	if x.rawGyro {
		accScale = StandardGravity * x.accRange / 32768
		gyroScale = x.gyroRange / 32768 * math.Pi / 180
	}
	out := make([]IMU, len(b)/entry)
	for i := range out {
		e := b[i*entry:]
		s := IMU{T: float64(int64(binary.LittleEndian.Uint64(e))-x.firstFrame)/perSecond - shift}
		var a [6]float64
		for k := range a {
			if x.rawGyro {
				a[k] = float64(binary.LittleEndian.Uint16(e[8+2*k:])) - 32768
			} else {
				a[k] = math.Float64frombits(binary.LittleEndian.Uint64(e[8+8*k:]))
			}
		}
		for k := range 3 {
			s.Accel[k] = a[k] * accScale
			s.Gyro[k] = a[3+k] * gyroScale
		}
		out[i] = s
	}
	return out, nil
}

var lensField = regexp.MustCompile(`^(.*_)(00|10)(_\d+\.insv)$`)

// Pair returns a two-file clip's front (_00_) and back (_10_) files, given
// either; both must exist.
func Pair(path string) (front, back string, err error) {
	dir, name := filepath.Split(path)
	m := lensField.FindStringSubmatch(name)
	if m == nil {
		return "", "", fmt.Errorf("%s: not an Insta360 lens file (VID_*_00_NNN.insv or VID_*_10_NNN.insv)", path)
	}
	front = filepath.Join(dir, m[1]+"00"+m[3])
	back = filepath.Join(dir, m[1]+"10"+m[3])
	for _, p := range []string{front, back} {
		if _, err := os.Stat(p); err != nil {
			return "", "", fmt.Errorf("%s: the clip's other lens file: %w", path, err)
		}
	}
	return front, back, nil
}
