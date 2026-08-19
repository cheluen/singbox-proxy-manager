package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const maxSingBoxDiagnosticBytes = 16 << 10

type boundedCommandOutput struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newBoundedCommandOutput(limit int) *boundedCommandOutput {
	if limit < 1 {
		limit = maxSingBoxDiagnosticBytes
	}
	return &boundedCommandOutput{limit: limit, data: make([]byte, 0, limit)}
}

func (output *boundedCommandOutput) Write(data []byte) (int, error) {
	written := len(data)
	output.mu.Lock()
	defer output.mu.Unlock()
	if len(data) >= output.limit {
		output.data = append(output.data[:0], data[len(data)-output.limit:]...)
		return written, nil
	}
	if overflow := len(output.data) + len(data) - output.limit; overflow > 0 {
		copy(output.data, output.data[overflow:])
		output.data = output.data[:len(output.data)-overflow]
	}
	output.data = append(output.data, data...)
	return written, nil
}

func (output *boundedCommandOutput) String(limit int) string {
	if output == nil {
		return ""
	}
	output.mu.Lock()
	defer output.mu.Unlock()
	data := output.data
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
	}
	return strings.ToValidUTF8(string(data), "?")
}

func acquireExecutionSlot(
	ctx context.Context,
	slots chan struct{},
	operation string,
) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if slots == nil {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s could not start: %w", operation, ctx.Err())
	}
}
