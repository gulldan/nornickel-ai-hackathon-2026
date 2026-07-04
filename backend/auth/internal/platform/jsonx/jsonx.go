// Package jsonx is the project's JSON facade, backed by bytedance/sonic — a
// high-performance, low-allocation JSON library. Routing all JSON through here
// means the encoder is a single-line swap rather than a codebase-wide change,
// and it keeps the REST edge fast.
package jsonx

import (
	"fmt"
	"io"

	"github.com/bytedance/sonic"
)

// Marshal encodes v to JSON.
func Marshal(v any) ([]byte, error) {
	b, err := sonic.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jsonx: marshal: %w", err)
	}
	return b, nil
}

// Unmarshal decodes JSON into v.
func Unmarshal(data []byte, v any) error {
	if err := sonic.Unmarshal(data, v); err != nil {
		return fmt.Errorf("jsonx: unmarshal: %w", err)
	}
	return nil
}

// NewEncoder returns a streaming JSON encoder writing to w.
func NewEncoder(w io.Writer) sonic.Encoder {
	return sonic.ConfigDefault.NewEncoder(w)
}

// NewDecoder returns a streaming JSON decoder reading from r.
func NewDecoder(r io.Reader) sonic.Decoder {
	return sonic.ConfigDefault.NewDecoder(r)
}
