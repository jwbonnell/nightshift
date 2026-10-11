package runner

import "testing"

func TestTailBufferKeepsOnlyLastMaxBytes(t *testing.T) {
	tb := &tailBuffer{max: 10}
	n, err := tb.Write([]byte("0123456789abcdef"))
	if err != nil || n != 16 {
		t.Fatalf("Write returned (%d, %v), want (16, nil)", n, err)
	}
	if tb.String() != "6789abcdef" {
		t.Errorf("tailBuffer = %q, want last 10 bytes", tb.String())
	}
}
