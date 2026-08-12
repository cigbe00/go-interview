package store_test

import (
	"github.com/maoni/backend-takehome/internal/store"
	"testing"
)

func TestSeededBusinessExistsInRawStore(t *testing.T) {
	st := store.NewMemoryStore()
	b, err := st.GetBusinessRaw("biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Lagos Bistro" {
		t.Fatalf("unexpected %+v", b)
	}
}
