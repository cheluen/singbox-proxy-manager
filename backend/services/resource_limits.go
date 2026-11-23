package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const maxSingBoxDiagnosticBytes = 4 << 10

type boundedCommandOutput struct {
	mu   sync.Mutex
	data []byte
}

func newBoundedCommandOutput() *boundedCommandOutput {
	return &boundedCommandOutput{data: make([]byte, 0, maxSingBoxDiagnosticBytes)}
}

func (output *boundedCommandOutput) Write(data []byte) (int, error) {
	written := len(data)
	output.mu.Lock()
	defer output.mu.Unlock()
	if len(data) >= maxSingBoxDiagnosticBytes {
		output.data = append(output.data[:0], data[len(data)-maxSingBoxDiagnosticBytes:]...)
		return written, nil
	}
	if overflow := len(output.data) + len(data) - maxSingBoxDiagnosticBytes; overflow > 0 {
		copy(output.data, output.data[overflow:])
		output.data = output.data[:len(output.data)-overflow]
	}
	output.data = append(output.data, data...)
	return written, nil
}

func (output *boundedCommandOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return strings.ToValidUTF8(string(output.data), "?")
}

func acquireExecutionSlot(
	ctx context.Context,
	slots chan struct{},
	operation string,
) (func(), error) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s could not start: %w", operation, ctx.Err())
	}
}
