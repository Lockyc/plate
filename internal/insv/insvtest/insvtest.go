// Package insvtest writes Insta360 trailers, so tests and quality fixtures
// can make an .insv clip from any video without a camera.
package insvtest

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"

	"github.com/lockyc/plate/internal/insv"
)

// Options says how Write states what Meta cannot.
type Options struct {
	// Offset is the lens calibration string, written verbatim; m.Lenses is
	// not written.
	Offset string
	// Raw writes the IMU as u16 counts with microsecond timestamps, as an X3
	// does, against AccRange (g) and GyroRange (°/s); f64 values with
	// millisecond timestamps otherwise.
	Raw                 bool
	AccRange, GyroRange uint32
	// Frames, when set, writes an exposure record of that many frames at
	// m.FPS from the first frame.
	Frames int
}

// Write appends a trailer stating m to the file at path: an ExtraMetadata
// record with m's serial, model, frame size and frame rate (rounded to a
// whole number, as the camera states it), m.IMU turned from the camera's
// axes onto m.Model's sensor axes, and the records o asks for.
func Write(path string, m insv.Meta, o Options) error {
	unit := 1e3
	if o.Raw {
		unit = 1e6
	}
	// Timestamps are unsigned: a sample before the first frame moves the
	// first frame's timestamp on instead.
	var base int64
	for _, s := range m.IMU {
		if t := int64(math.Round(s.T * unit)); -t > base {
			base = -t
		}
	}

	var meta []byte
	meta = pbBytes(meta, insv.FieldSerial, []byte(m.Serial))
	meta = pbBytes(meta, insv.FieldCameraType, []byte(m.Model))
	meta = pbBytes(meta, insv.FieldOffset, []byte(o.Offset))
	dim := pbVarint(pbVarint(nil, 1, uint64(m.Width)), 2, uint64(m.Height))
	meta = pbBytes(meta, insv.FieldDimension, dim)
	meta = pbVarint(meta, insv.FieldFrameRate, uint64(math.Round(m.FPS)))
	meta = pbVarint(meta, insv.FieldFirstFrameTime, uint64(base))
	if o.Raw {
		meta = pbVarint(meta, insv.FieldIsRawGyro, 1)
		cfg := pbVarint(pbVarint(nil, 1, uint64(o.AccRange)), 2, uint64(o.GyroRange))
		meta = pbBytes(meta, insv.FieldGyroConfig, cfg)
	}

	var t []byte
	if len(m.IMU) > 0 {
		axes, ok := insv.Axes[m.Model]
		if !ok {
			return fmt.Errorf("the IMU axes of a %q are not known", m.Model)
		}
		imu, err := imuRecord(m.IMU, axes, o, unit, base)
		if err != nil {
			return err
		}
		t = appendRecord(t, insv.RecordIMU, insv.FormatBinary, imu)
	}
	if o.Frames > 0 {
		var exp []byte
		for i := range o.Frames {
			ts := base + int64(math.Round(float64(i)/m.FPS*unit))
			exp = binary.LittleEndian.AppendUint64(exp, uint64(ts))
			exp = binary.LittleEndian.AppendUint64(exp, math.Float64bits(1/m.FPS))
		}
		t = appendRecord(t, insv.RecordExposure, insv.FormatBinary, exp)
	}
	t = appendRecord(t, insv.RecordMetadata, insv.FormatProtobuf, meta)
	t = append(t, make([]byte, 32)...)
	t = binary.LittleEndian.AppendUint32(t, uint32(len(t)+insv.FooterSize-32))
	t = binary.LittleEndian.AppendUint32(t, 3) // the version an X3 writes
	t = append(t, insv.Magic...)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(t); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	return f.Close()
}

// imuRecord lays out samples as record 3: accelerometer then gyroscope, in
// g and rad/s, or as raw counts.
func imuRecord(samples []insv.IMU, axes insv.Orientation, o Options, unit float64, base int64) ([]byte, error) {
	accScale, gyroScale := insv.StandardGravity, 1.0
	size := insv.SampleSize
	if o.Raw {
		accScale, gyroScale = insv.RawScales(float64(o.AccRange), float64(o.GyroRange))
		size = insv.RawSampleSize
	}
	b := make([]byte, 0, len(samples)*size)
	for _, s := range samples {
		b = binary.LittleEndian.AppendUint64(b, uint64(int64(math.Round(s.T*unit))+base))
		a, g := axes.ToSensor(s.Accel), axes.ToSensor(s.Gyro)
		for _, v := range []float64{a[0] / accScale, a[1] / accScale, a[2] / accScale, g[0] / gyroScale, g[1] / gyroScale, g[2] / gyroScale} {
			if !o.Raw {
				b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
				continue
			}
			c := math.Round(v) + insv.RawZero
			if c < 0 || c > math.MaxUint16 {
				return nil, fmt.Errorf("IMU value %v is outside the raw range", v)
			}
			b = binary.LittleEndian.AppendUint16(b, uint16(c))
		}
	}
	return b, nil
}

// appendRecord appends data and the descriptor that closes it.
func appendRecord(t []byte, id, format byte, data []byte) []byte {
	t = append(t, data...)
	t = append(t, format, id)
	return binary.LittleEndian.AppendUint32(t, uint32(len(data)))
}

func pbVarint(b []byte, field int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3)
	return binary.AppendUvarint(b, v)
}

func pbBytes(b []byte, field int, data []byte) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|2)
	b = binary.AppendUvarint(b, uint64(len(data)))
	return append(b, data...)
}
