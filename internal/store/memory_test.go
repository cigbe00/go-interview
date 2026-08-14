package store_test

import (
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
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

func TestSaveAndListReviews(t *testing.T) {
	st := store.NewMemoryStore()

	count, avg := st.ReviewStats("biz_1")
	if count != 3 {
		t.Fatalf("expected initial count to be 3, got %d", count)
	}

	err := st.SaveReview(model.Review{
		BusinessID: "biz_1",
		UserID:     "user_4",
		Rating:     1,
		Body:       "Bad service",
		CreatedAt:  time.Now(),
	})

	if err != nil {
		t.Fatalf("unexpected error saving review: %v", err)
	}

	count, avg = st.ReviewStats("biz_1")
	if count != 4 {
		t.Errorf("expected count 4 after save, got %d", count)
	}

	if avg != 3.5 {
		t.Errorf("expected precision avg 3.5, got %f", avg)
	}

	reviews := st.ListReviews("biz_1", 1, 2)
	if len(reviews) != 2 {
		t.Errorf("expected 2 reviews on page 1, got %d", len(reviews))
	}
}

func TestGetBusinessById(t *testing.T) {
	st := store.NewMemoryStore()

	b, err := st.GetBusiness("biz_1")
	if err != nil {
		t.Fatalf("failed lookup by ID: %v", err)
	}
	if b.ID != "biz_1" {
		t.Errorf("expected biz_1, got %s", b.ID)
	}
}
