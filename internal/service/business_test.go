package service_test

import (
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
	"testing"
)

func TestBusinessByID(t *testing.T) {
	svc := &service.BusinessService{Store: store.NewMemoryStore()}
	if _, err := svc.GetBusiness("biz_1"); err != nil {
		t.Fatalf("expected biz_1 lookup to succeed: %v", err)
	}
}
func TestAverageRatingPreservesFraction(t *testing.T) {
	svc := &service.BusinessService{Store: store.NewMemoryStore()}
	b, err := svc.GetBusiness("biz_1")
	if err != nil {
		t.Fatal(err)
	}
	want := 13.0 / 3.0
	if b.Average != want {
		t.Fatalf("average=%v want=%v", b.Average, want)
	}
}
func TestCreateReviewChangesCount(t *testing.T) {
	st := store.NewMemoryStore()
	svc := &service.BusinessService{Store: st}
	before, _ := st.ReviewStats("biz_1")
	if _, err := svc.CreateReview("biz_1", "candidate", 5, "new review"); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ReviewStats("biz_1")
	if after != before+1 {
		t.Fatalf("count after=%d before=%d", after, before)
	}
}
func TestFirstPageStartsWithNewestReviews(t *testing.T) {
	svc := &service.BusinessService{Store: store.NewMemoryStore()}
	rows := svc.ListReviews("biz_1", 1, 2)
	if len(rows) != 2 {
		t.Fatalf("len=%d want=2", len(rows))
	}
	if rows[0].ID != "rev_3" {
		t.Fatalf("first id=%s want rev_3", rows[0].ID)
	}
}
