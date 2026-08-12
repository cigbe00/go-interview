package store_test

import (
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
	"testing"
	"time"
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

func TestGetBusinessByDocumentedID(t *testing.T) {
	st := store.NewMemoryStore()
	b, err := st.GetBusiness("biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != "biz_1" {
		t.Fatalf("unexpected %+v", b)
	}
	if _, err := st.GetBusiness("lagos-bistro"); err == nil {
		t.Fatal("slug lookups should not be treated as ids")
	}
}

func TestSaveReviewUpdatesStatsWithPrecision(t *testing.T) {
	st := store.NewMemoryStore()
	r := model.Review{ID: "rev_new", BusinessID: "biz_1", UserID: "user_9", Rating: 5, CreatedAt: time.Now()}
	if err := st.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	// seeded 5,4,4 plus 5 => 4 reviews averaging (18/4) = 4.5
	count, avg := st.ReviewStats("biz_1")
	if count != 4 {
		t.Fatalf("count = %d, want 4", count)
	}
	if avg != 4.5 {
		t.Fatalf("avg = %v, want 4.5", avg)
	}
}

func TestListReviewsDoesNotSkipFirstPage(t *testing.T) {
	st := store.NewMemoryStore()
	page1 := st.ListReviews("biz_1", 1, 2)
	if len(page1) != 2 {
		t.Fatalf("page 1 has %d reviews, want 2", len(page1))
	}
	page2 := st.ListReviews("biz_1", 2, 2)
	if len(page2) != 1 {
		t.Fatalf("page 2 has %d reviews, want 1", len(page2))
	}
}
