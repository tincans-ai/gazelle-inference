package hb

import (
	"bytes"
	"context"
	"github.com/rs/xid"
	"sort"
	"sync"

	"log/slog"
)

// HierarchicalBuffer implements a hierarchical buffer structure.
type HierarchicalBuffer struct {
	buffer         map[xid.ID]map[xid.ID]*bytes.Buffer
	mu             sync.RWMutex // protects buffer
	overflowBuffer *bytes.Buffer

	maxLength map[xid.ID]map[xid.ID]int

	eofMap map[xid.ID]xid.ID
	eofMu  sync.RWMutex // protects eofMap
}

// NewHierarchicalBuffer creates a new HierarchicalBuffer and initializes it.
func NewHierarchicalBuffer() *HierarchicalBuffer {
	return &HierarchicalBuffer{
		buffer:         make(map[xid.ID]map[xid.ID]*bytes.Buffer),
		overflowBuffer: new(bytes.Buffer),
		maxLength:      make(map[xid.ID]map[xid.ID]int),
		eofMap:         make(map[xid.ID]xid.ID),
	}
}

func (hb *HierarchicalBuffer) GetOverflowBuffer() *bytes.Buffer {
	return hb.overflowBuffer
}

func (hb *HierarchicalBuffer) SetEofMap(gazelleID xid.ID, synthesizerRequestID xid.ID) {
	hb.eofMu.Lock()
	defer hb.eofMu.Unlock()

	hb.eofMap[gazelleID] = synthesizerRequestID
}

func (hb *HierarchicalBuffer) GetEofForRequestID(gazelleID xid.ID) xid.ID {
	hb.eofMu.RLock()
	defer hb.eofMu.RUnlock()

	return hb.eofMap[gazelleID]
}

// Add adds data to the buffer. It adds or updates the entry for the given
// transcriberRequestID and synthesizerRequestID.
func (hb *HierarchicalBuffer) Add(gazelleID xid.ID, synthesizerRequestID xid.ID, data []byte) {
	hb.mu.Lock()
	defer hb.mu.Unlock()

	if _, exists := hb.buffer[gazelleID]; !exists {
		hb.buffer[gazelleID] = make(map[xid.ID]*bytes.Buffer)
	}

	if _, exists := hb.buffer[gazelleID][synthesizerRequestID]; !exists {
		hb.buffer[gazelleID][synthesizerRequestID] = new(bytes.Buffer)
	}

	hb.buffer[gazelleID][synthesizerRequestID].Write(data)

	_, exists := hb.maxLength[gazelleID]
	if !exists {
		hb.maxLength[gazelleID] = make(map[xid.ID]int)
	}
	hb.maxLength[gazelleID][synthesizerRequestID] += len(data)
}

// Clear removes all entries for the given transcriberRequestID.
func (hb *HierarchicalBuffer) Clear(transcriberRequestID xid.ID) {
	hb.mu.Lock()
	defer hb.mu.Unlock()

	delete(hb.buffer, transcriberRequestID)
	hb.overflowBuffer.Reset()
}

func (hb *HierarchicalBuffer) TotalLen() int {
	hb.mu.RLock()
	defer hb.mu.RUnlock()

	result := hb.overflowBuffer.Len()

	for _, lmMap := range hb.buffer {
		for _, buf := range lmMap {
			result += buf.Len()
		}
	}

	return result
}

func sortSynthesizerIDs(ids []xid.ID) {
	sort.Slice(ids, func(i, j int) bool {
		return ids[i].String() < ids[j].String()
	})
}

// NextZC retrieves and removes up to n bytes, up to a zero crossing, from the buffer for the given transcriberRequestID.
// This returns the bytes read, a boolean indicating whether we're done, and the last synthesizerRequestID read.
func (hb *HierarchicalBuffer) NextZC(ctx context.Context, gazelleID xid.ID, n int) ([]byte, bool, xid.ID) {
	hb.mu.Lock()
	defer hb.mu.Unlock()

	// logger := logutil.LoggerFromContext(ctx)
	// start by initializing with the previous overflow buffer
	tmpResult := hb.overflowBuffer.Next(n)
	remaining := n - len(tmpResult)
	// TODO: check this more carefully? but it also _should not trigger_
	if remaining <= 0 {
		slog.Warn("overflow buffer was too big, this should not happen")
		return tmpResult, false, xid.NilID()
	}

	lastKey := xid.NilID()
	if lmMap, exists := hb.buffer[gazelleID]; exists {
		// Get sorted languageModelRequestID keys
		keys := make([]xid.ID, 0, len(lmMap))
		for k := range lmMap {
			keys = append(keys, k)
		}
		sortSynthesizerIDs(keys)

		// Fetch bytes from each buffer in sorted order
		for _, k := range keys {
			lastKey = k
			buf := lmMap[k]
			if buf == nil {
				continue
			}

			bytesRead := buf.Next(remaining)
			tmpResult = append(tmpResult, bytesRead...)
			remaining -= len(bytesRead)

			if remaining <= 0 {
				break
			}
		}
	}
	eofSynthesizerID := hb.GetEofForRequestID(gazelleID)
	// we're done if the last synthesizerRequestID is the EOF
	// and if we've read all the bytes for that synthesizerRequestID
	done := lastKey == eofSynthesizerID && remaining >= 0

	return tmpResult, done, lastKey
}

func (hb *HierarchicalBuffer) Len(transcriberRequestID xid.ID) int {
	hb.mu.RLock()
	defer hb.mu.RUnlock()

	result := hb.overflowBuffer.Len()

	if lmMap, exists := hb.buffer[transcriberRequestID]; exists {
		for _, buf := range lmMap {
			result += buf.Len()
		}
	}

	return result
}
