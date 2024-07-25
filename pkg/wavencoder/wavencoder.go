package wavencoder

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	gaudio "github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

type WavEncoder struct {
	sampleRate  int
	bitDepth    int
	numChannels int
	audioFormat int
}

func NewWavEncoder(sampleRate int, bitDepth int, numChannels int, audioFormat int) *WavEncoder {
	return &WavEncoder{
		sampleRate:  sampleRate,
		bitDepth:    bitDepth,
		numChannels: numChannels,
		audioFormat: audioFormat,
	}
}

// CustomIOWriteSeeker implements io.WriteSeeker
type CustomIOWriteSeeker struct {
	buf []byte
	pos int
}

func NewCustomIOWriteSeeker() *CustomIOWriteSeeker {
	return &CustomIOWriteSeeker{
		buf: make([]byte, 0),
		pos: 0,
	}
}

// Write appends an input buffer to the internal buffer
func (m *CustomIOWriteSeeker) Write(p []byte) (n int, err error) {
	minCap := m.pos + len(p)
	if minCap > cap(m.buf) { // Make sure buf has enough capacity:
		buf2 := make([]byte, len(m.buf), minCap+len(p)) // add some extra
		copy(buf2, m.buf)
		m.buf = buf2
	}
	if minCap > len(m.buf) {
		m.buf = m.buf[:minCap]
	}
	copy(m.buf[m.pos:], p)
	m.pos += len(p)
	return len(p), nil
}

// Seek sets the offset for the next Read or Write on the in-memory buffer to offset, interpreted according to whence:
// 0 means relative to the origin of the buffer, 1 means relative to the current offset, and 2 means relative to the end.
// It returns the new offset and an error, if any.
func (m *CustomIOWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	newPos, offs := 0, int(offset)
	switch whence {
	case io.SeekStart:
		newPos = offs
	case io.SeekCurrent:
		newPos = m.pos + offs
	case io.SeekEnd:
		newPos = len(m.buf) + offs
	}
	if newPos < 0 {
		return 0, errors.New("negative result pos")
	}
	m.pos = newPos
	return int64(newPos), nil
}

// Bytes returns the underlying buffer
func (m *CustomIOWriteSeeker) Bytes() []byte {
	return m.buf
}

// NewAudioIntBuffer reads a wav file and returns an IntBuffer
// This is necessary to work with go-audio's wav encoder
func NewAudioIntBuffer(r io.Reader, sampleRate int, numChannels int) (*gaudio.IntBuffer, error) {
	buf := gaudio.IntBuffer{
		Format: &gaudio.Format{
			NumChannels: numChannels,
			SampleRate:  sampleRate,
		},
	}
	for {
		var sample int16
		err := binary.Read(r, binary.LittleEndian, &sample)
		switch {
		case err == io.EOF:
			return &buf, nil
		case err != nil:
			return nil, err
		}
		buf.Data = append(buf.Data, int(sample))
	}
}

func (ac *WavEncoder) ConvertAudio(ctx context.Context, audio []byte) ([]byte, error) {
	buf, err := NewAudioIntBuffer(bytes.NewReader(audio), ac.sampleRate, ac.numChannels)
	if err != nil {
		return nil, fmt.Errorf("error creating audio buffer: %w", err)
	}

	w := NewCustomIOWriteSeeker()
	encoder := wav.NewEncoder(w, ac.sampleRate, ac.bitDepth, ac.numChannels, ac.audioFormat)
	err = encoder.Write(buf)
	if err != nil {
		return nil, fmt.Errorf("error writing to encoder: %w", err)
	}

	err = encoder.Close()
	if err != nil {
		return nil, fmt.Errorf("error closing encoder: %w", err)
	}

	return w.Bytes(), nil
}
