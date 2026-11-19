package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}

// TestMul is the deliberate gap: Mul is not implemented yet, so the package does not compile
// under `go test` until an agent adds it to calc.go.
func TestMul(t *testing.T) {
	if got := Mul(3, 4); got != 12 {
		t.Fatalf("Mul(3, 4) = %d, want 12", got)
	}
}
