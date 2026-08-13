package store_test

import (
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
	"math"
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

func TestReviewStatsPreservePrecisionAndSavedReviews(t *testing.T) {
	st := store.NewMemoryStore()
	count, average := st.ReviewStats("biz_1")
	if count != 3 || math.Abs(average-(13.0/3.0)) > 0.0001 {
		t.Fatalf("before save: count=%d average=%f", count, average)
	}
	if err := st.SaveReview(model.Review{ID: "rev_4", BusinessID: "biz_1", Rating: 5}); err != nil {
		t.Fatal(err)
	}
	count, average = st.ReviewStats("biz_1")
	if count != 4 || math.Abs(average-4.5) > 0.0001 {
		t.Fatalf("after save: count=%d average=%f", count, average)
	}
}

func TestUpsertUserUsesGoogleSubjectAsStableKey(t *testing.T) {
	st := store.NewMemoryStore()
	first := st.UpsertUser(model.User{GoogleID: "google-1", Email: "same@example.com"})
	second := st.UpsertUser(model.User{GoogleID: "google-2", Email: "same@example.com"})
	if first.ID == second.ID {
		t.Fatal("different Google subjects were merged by email")
	}
}
