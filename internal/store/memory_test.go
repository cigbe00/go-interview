package store_test

import (
	"errors"
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

// Symptom 1: a seeded business returned 404 for its documented ID because the
// lookup matched on Slug.
func TestGetBusinessResolvesDocumentedID(t *testing.T) {
	st := store.NewMemoryStore()
	b, err := st.GetBusiness("biz_1")
	if err != nil {
		t.Fatalf("lookup by documented ID failed: %v", err)
	}
	if b.ID != "biz_1" || b.Name != "Lagos Bistro" {
		t.Fatalf("unexpected %+v", b)
	}
	if _, err := st.GetBusiness("does_not_exist"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// Symptom 2: saved reviews were written to a different collection key, so they
// never reached ListReviews or ReviewStats.
func TestSaveReviewIsVisibleToListAndStats(t *testing.T) {
	st := store.NewMemoryStore()
	countBefore, _ := st.ReviewStats("biz_1")

	err := st.SaveReview(model.Review{
		ID: "rev_new", BusinessID: "biz_1", UserID: "user_9", Rating: 5,
		Body: "New", CreatedAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	countAfter, _ := st.ReviewStats("biz_1")
	if countAfter != countBefore+1 {
		t.Fatalf("stats did not see the saved review: before=%d after=%d", countBefore, countAfter)
	}
	page, total := st.ListReviews("biz_1", 1, 10)
	if total != countAfter {
		t.Fatalf("total = %d, want %d", total, countAfter)
	}
	if len(page) == 0 || page[0].ID != "rev_new" {
		t.Fatalf("newest review missing from first page: %+v", page)
	}
}

// Symptom 3: the average was computed with integer division before the float
// conversion, so 5+4+4 over 3 reviews reported 4 instead of 4.333…
func TestReviewStatsKeepsFractionalAverage(t *testing.T) {
	st := store.NewMemoryStore()
	count, avg := st.ReviewStats("biz_1")
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	const want = 13.0 / 3.0
	if diff := avg - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("average = %v, want %v", avg, want)
	}

	if count, avg := st.ReviewStats("biz_2"); count != 0 || avg != 0 {
		t.Fatalf("empty business stats = (%d, %v), want (0, 0)", count, avg)
	}
}

// Symptom 4: the offset was page*limit against a 1-based page, so the first
// page silently skipped the newest records.
func TestListReviewsPaginationIsOneBasedAndComplete(t *testing.T) {
	st := store.NewMemoryStore()

	first, total := st.ListReviews("biz_1", 1, 2)
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(first) != 2 {
		t.Fatalf("page 1 returned %d reviews, want 2", len(first))
	}
	// Newest first: rev_3 (now), rev_2 (-1h), rev_1 (-2h).
	if first[0].ID != "rev_3" || first[1].ID != "rev_2" {
		t.Fatalf("page 1 = %s,%s want rev_3,rev_2", first[0].ID, first[1].ID)
	}

	second, _ := st.ListReviews("biz_1", 2, 2)
	if len(second) != 1 || second[0].ID != "rev_1" {
		t.Fatalf("page 2 = %+v, want just rev_1", second)
	}

	if page, _ := st.ListReviews("biz_1", 3, 2); len(page) != 0 {
		t.Fatalf("page past the end = %+v, want empty", page)
	}

	// Every record appears exactly once across the pages.
	seen := map[string]int{}
	for _, r := range append(append([]model.Review{}, first...), second...) {
		seen[r.ID]++
	}
	if len(seen) != 3 {
		t.Fatalf("pages covered %d distinct reviews, want 3: %v", len(seen), seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("review %s appeared %d times across pages", id, n)
		}
	}
}

// Reviews written in the same instant must have a total order, otherwise a
// record can be skipped on one page and repeated on the next.
func TestListReviewsOrderIsStableForIdenticalTimestamps(t *testing.T) {
	st := store.NewMemoryStore()
	ts := time.Now().UTC().Add(time.Hour)
	for _, id := range []string{"rev_b", "rev_a", "rev_c"} {
		if err := st.SaveReview(model.Review{ID: id, BusinessID: "biz_2", Rating: 4, CreatedAt: ts}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"rev_a", "rev_b", "rev_c"}
	for i := 0; i < 20; i++ {
		page, total := st.ListReviews("biz_2", 1, 10)
		if total != 3 {
			t.Fatalf("total = %d, want 3", total)
		}
		for j, r := range page {
			if r.ID != want[j] {
				t.Fatalf("iteration %d: order = %+v, want %v", i, page, want)
			}
		}
	}
}

func TestUpsertUserKeysOnGoogleSubjectNotEmail(t *testing.T) {
	st := store.NewMemoryStore()

	created := st.UpsertUser(model.User{Email: "user@example.com", Name: "User", GoogleID: "google-sub-1"})
	if created.ID == "" {
		t.Fatal("expected a generated user ID")
	}

	// Same Google subject, changed email: still the same local account.
	again := st.UpsertUser(model.User{Email: "moved@example.com", Name: "User Renamed", GoogleID: "google-sub-1"})
	if again.ID != created.ID {
		t.Fatalf("same subject produced a new account: %s vs %s", again.ID, created.ID)
	}
	if again.Email != "moved@example.com" || again.Name != "User Renamed" {
		t.Fatalf("profile changes were not applied: %+v", again)
	}

	// A different subject that reuses the freed email is a different account.
	other := st.UpsertUser(model.User{Email: "user@example.com", Name: "Someone Else", GoogleID: "google-sub-2"})
	if other.ID == created.ID {
		t.Fatal("a different Google subject was merged into an existing account")
	}
}

func TestUpsertUserLinksSubjectToAccountCreatedByEmail(t *testing.T) {
	st := store.NewMemoryStore()
	existing := st.UpsertUser(model.User{ID: "usr_legacy", Email: "legacy@example.com", Name: "Legacy"})

	linked := st.UpsertUser(model.User{Email: "legacy@example.com", Name: "Legacy", GoogleID: "google-sub-9"})
	if linked.ID != existing.ID {
		t.Fatalf("first Google sign-in duplicated the account: %s vs %s", linked.ID, existing.ID)
	}
	if linked.GoogleID != "google-sub-9" {
		t.Fatalf("google subject was not linked: %+v", linked)
	}

	// The link must hold on the next sign-in even if the email changes.
	again := st.UpsertUser(model.User{Email: "new@example.com", GoogleID: "google-sub-9"})
	if again.ID != existing.ID {
		t.Fatalf("link did not persist: %s vs %s", again.ID, existing.ID)
	}
}

func TestMarkEventProcessedIsSingleClaim(t *testing.T) {
	st := store.NewMemoryStore()
	if !st.MarkEventProcessed("evt_1") {
		t.Fatal("first claim should succeed")
	}
	if st.MarkEventProcessed("evt_1") {
		t.Fatal("second claim of the same event should fail")
	}
	if !st.MarkEventProcessed("evt_2") {
		t.Fatal("a different event should still be claimable")
	}
	// An empty ID would collapse every event onto one key.
	if st.MarkEventProcessed("") {
		t.Fatal("empty event ID must not be claimable")
	}
}

func TestGetSubscriptionByReference(t *testing.T) {
	st := store.NewMemoryStore()
	st.PutSubscription(model.Subscription{UserID: "usr_1", Status: "pending", Reference: "ref_abc", PlanCode: "PLN_x"})

	sub, err := st.GetSubscriptionByReference("ref_abc")
	if err != nil {
		t.Fatal(err)
	}
	if sub.UserID != "usr_1" {
		t.Fatalf("unexpected %+v", sub)
	}
	if _, err := st.GetSubscriptionByReference("ref_unknown"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestConcurrentReviewWritesAreSafe(t *testing.T) {
	st := store.NewMemoryStore()
	const writers = 25
	done := make(chan struct{})
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			_ = st.SaveReview(model.Review{
				ID: "rev_c" + time.Duration(i).String(), BusinessID: "biz_2",
				Rating: 1 + i%5, CreatedAt: time.Now().UTC(),
			})
			st.ListReviews("biz_2", 1, 10)
			st.ReviewStats("biz_2")
		}(i)
	}
	for i := 0; i < writers; i++ {
		<-done
	}
	if count, _ := st.ReviewStats("biz_2"); count != writers {
		t.Fatalf("count = %d, want %d", count, writers)
	}
}
