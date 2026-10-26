package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResourceBudgetDoesNotTreatUnlimitedOrInvalidAsZero(t *testing.T) {
	root := t.TempDir()
	used, limit := filepath.Join(root, "used"), filepath.Join(root, "limit")
	os.WriteFile(used, []byte("80"), 0600)
	os.WriteFile(limit, []byte("100"), 0600)
	if remaining, cap, ok := budgetRemaining(used, limit); !ok || remaining != 20 || cap != 100 {
		t.Fatal("wrong cgroup headroom")
	}
	os.WriteFile(limit, []byte("max"), 0600)
	if _, _, ok := budgetRemaining(used, limit); ok {
		t.Fatal("unlimited interpreted as exhausted")
	}
	os.WriteFile(limit, []byte("70"), 0600)
	if remaining, _, ok := budgetRemaining(used, limit); !ok || remaining != 0 {
		t.Fatal("over-limit subtraction overflow")
	}
}
