package memory

import (
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// TestLogStoreDeleteBefore verifies the retention cleanup primitive: logs older
// than the cutoff are deleted, newer ones kept, and the deleted count is returned.
func TestLogStoreDeleteBefore(t *testing.T) {
	st := New()
	now := time.Now().UnixMilli()
	st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "old", CreatedAt: now - 48*60*60*1000},
		{ID: "keep", CreatedAt: now},
	})
	n, err := st.Logs().DeleteBefore(now - 24*60*60*1000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("DeleteBefore deleted %d, want 1", n)
	}
	page, _ := st.Logs().Query(storage.LogQuery{Limit: 10})
	if len(page.Items) != 1 || page.Items[0].ID != "keep" {
		t.Errorf("after DeleteBefore: %+v", page.Items)
	}
}
