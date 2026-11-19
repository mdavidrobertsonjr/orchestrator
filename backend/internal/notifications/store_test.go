package notifications

import (
	"errors"
	"sync"
	"testing"
)

func TestMemoryDeliveryRetriesReuseOneRecordAndAttempt(t *testing.T) {
	store := NewMemoryStore()
	params := CreateDeliveryParams{JobID: "job", Kind: "alert", Provider: "smtp", Recipients: []string{"b@example.com", "a@example.com"}}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			delivery, err := store.Create(params)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = store.MarkAttempt(delivery.ID)
			if err != nil && !errors.Is(err, ErrAlreadyAttempted) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	deliveries, _ := store.List()
	if len(deliveries) != 1 || deliveries[0].Attempts != 1 {
		t.Fatalf("unexpected deliveries: %+v", deliveries)
	}
	params.Recipients = []string{"a@example.com", "b@example.com"}
	repeated, err := store.Create(params)
	if err != nil || repeated.ID != deliveries[0].ID {
		t.Fatalf("reordered recipients: %+v, %v", repeated, err)
	}
}
