package ingest

import (
	"strings"
	"testing"
)

func TestLineReader(t *testing.T) {
	long := strings.Repeat("x", 3<<20) // más largo que el buffer de 1MB
	input := "a\r\n\n  \n" + long + "\nb\r\n" + long + "c"
	lr := newLineReader(strings.NewReader(input))
	var got []string
	for lr.Next() {
		got = append(got, string(lr.Bytes()))
	}
	if err := lr.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
	want := []string{"a", long, "b", long + "c"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: len %d, want len %d", i, len(got[i]), len(want[i]))
		}
	}
}

func TestLineReader_TooLong(t *testing.T) {
	lr := newLineReader(strings.NewReader(strings.Repeat("x", maxLineBytes+1)))
	for lr.Next() {
	}
	if lr.Err() == nil {
		t.Fatal("Err() = nil, want line-too-long error")
	}
}
