package catalog

import "testing"

func TestGroupCycle(t *testing.T) {
	if nextGroup(groupColorTokens) != groupTextAttrs {
		t.Fatal("nextGroup should wrap to first")
	}
	if prevGroup(groupTextAttrs) != groupColorTokens {
		t.Fatal("prevGroup should wrap to last")
	}
	if nextGroup(groupTextAttrs) != groupSemanticRoles {
		t.Fatal("nextGroup should advance")
	}
}

func TestClampOffset(t *testing.T) {
	if got := clampOffset(100, 10, 4); got != 6 { // max = 10-4
		t.Fatalf("clamp high = %d, want 6", got)
	}
	if got := clampOffset(-5, 10, 4); got != 0 {
		t.Fatalf("clamp low = %d, want 0", got)
	}
	if got := clampOffset(3, 2, 4); got != 0 { // content fits, no scroll
		t.Fatalf("clamp fits = %d, want 0", got)
	}
}

func TestLineCount(t *testing.T) {
	if lineCount("") != 0 || lineCount("a") != 1 || lineCount("a\nb\nc") != 3 {
		t.Fatal("lineCount wrong")
	}
}
