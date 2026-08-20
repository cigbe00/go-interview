package store_test

import (
	"errors"
	"math"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
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

func TestBusinessLookupUsesDocumentedID(t *testing.T) {
	st := store.NewMemoryStore()
	if _, err := st.GetBusiness("biz_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetBusiness("lagos-bistro"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("slug lookup error = %v, want ErrNotFound", err)
	}
}

func TestSaveReviewUpdatesStatsWithPrecision(t *testing.T) {
	st := store.NewMemoryStore()
	err := st.SaveReview(model.Review{
		ID: "rev_4", BusinessID: "biz_1", UserID: "user_4", Rating: 3, CreatedAt: time.Now().UTC().Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	count, average := st.ReviewStats("biz_1")
	if count != 4 || math.Abs(average-4) > 0.000001 {
		t.Fatalf("count=%d average=%f", count, average)
	}
	reviews := st.ListReviews("biz_1", 1, 10)
	if len(reviews) != 4 || reviews[0].ID != "rev_4" {
		t.Fatalf("reviews=%+v", reviews)
	}
}

func TestReviewStatsKeepsFractionalAverage(t *testing.T) {
	st := store.NewMemoryStore()
	count, average := st.ReviewStats("biz_1")
	if count != 3 || math.Abs(average-(13.0/3.0)) > 0.000001 {
		t.Fatalf("count=%d average=%f", count, average)
	}
}

func TestReviewPaginationIsOneBased(t *testing.T) {
	st := store.NewMemoryStore()
	page1 := st.ListReviews("biz_1", 1, 2)
	page2 := st.ListReviews("biz_1", 2, 2)
	if len(page1) != 2 || page1[0].ID != "rev_3" || page1[1].ID != "rev_2" {
		t.Fatalf("page1=%+v", page1)
	}
	if len(page2) != 1 || page2[0].ID != "rev_1" {
		t.Fatalf("page2=%+v", page2)
	}
}
