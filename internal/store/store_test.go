package store

import (
	"path/filepath"
	"testing"
)

func TestLastCheckinDayPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := st.LastCheckinDay(); got != "" {
		t.Fatalf("fresh LastCheckinDay = %q, want empty", got)
	}

	st.SetLastCheckinDay("2026-10-03")
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.LastCheckinDay(); got != "2026-10-03" {
		t.Fatalf("reopened LastCheckinDay = %q, want 2026-10-03", got)
	}
}

func TestSetLastCheckinDaySameValueKeepsClean(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.SetLastCheckinDay("2026-10-03")
	st.mu.Lock()
	st.dirty = false
	st.mu.Unlock()

	st.SetLastCheckinDay("2026-10-03")
	st.mu.RLock()
	dirty := st.dirty
	st.mu.RUnlock()
	if dirty {
		t.Fatal("setting the same day marked the store dirty")
	}
}
