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

// Write appends a trailer stating m to the file at path: an ExtraMetadata
// record with m's serial, model, frame size and frame rate (rounded to a
// whole number, as the camera states it) and offset as the lens calibration
// string verbatim (m.Lenses is not written), then m.IMU as f64 samples at
// millisecond resolution.
func Write(path string, m insv.Meta, offset string) error {
	var meta []byte
	meta = pbBytes(meta, insv.FieldSerial, []byte(m.Serial))
	meta = pbBytes(meta, insv.FieldCameraType, []byte(m.Model))
	meta = pbBytes(meta, insv.FieldOffset, []byte(offset))
	dim := pbVarint(pbVarint(nil, 1, uint64(m.Width)), 2, uint64(m.Height))
	meta = pbBytes(meta, insv.FieldDimension, dim)
	meta = pbVarint(meta, insv.FieldFrameRate, uint64(math.Round(m.FPS)))

	// Timestamps are unsigned: a sample before the first frame moves the
	// first frame's timestamp on instead.
	var base int64
	for _, s := range m.IMU {
		if ms := int64(math.Round(s.T * 1e3)); -ms > base {
			base = -ms
		}
	}
	meta = pbVarint(meta, insv.FieldFirstFrameTime, uint64(base))

	imu := make([]byte, 0, len(m.IMU)*(8+6*8))
	for _, s := range m.IMU {
		imu = binary.LittleEndian.AppendUint64(imu, uint64(int64(math.Round(s.T*1e3))+base))
		for _, v := range s.Accel {
			imu = binary.LittleEndian.AppendUint64(imu, math.Float64bits(v/insv.StandardGravity))
		}
		for _, v := range s.Gyro {
			imu = binary.LittleEndian.AppendUint64(imu, math.Float64bits(v))
		}
	}

	var t []byte
	t = appendRecord(t, insv.RecordIMU, insv.FormatBinary, imu)
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
