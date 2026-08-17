package server

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestThreadSafeTableHolderSwapsAndMatches(t *testing.T) {
	first := newHolderTestTable("first")
	second := newHolderTestTable("second")
	holder := NewThreadSafeTableHolder(nil)

	if _, err := holder.Match(httptest.NewRequest("GET", "http://seam.test/items/42", nil)); err == nil {
		t.Fatal("Match should fail before a route table is installed")
	}
	if err := holder.Swap(nil); err == nil {
		t.Fatal("Swap should reject a nil route table")
	}
	if err := holder.Swap(first); err != nil {
		t.Fatalf("Swap(first) returned error: %v", err)
	}

	match, err := holder.Match(httptest.NewRequest("GET", "http://seam.test/items/42", nil))
	if err != nil {
		t.Fatalf("Match(first) returned error: %v", err)
	}
	if match.Route.UpstreamTarget != "first" || match.PathParams["id"] != "42" {
		t.Fatalf("Match(first) = %#v, want first route with id=42", match)
	}

	if err := holder.Swap(second); err != nil {
		t.Fatalf("Swap(second) returned error: %v", err)
	}
	match, err = holder.Match(httptest.NewRequest("GET", "http://seam.test/items/42", nil))
	if err != nil {
		t.Fatalf("Match(second) returned error: %v", err)
	}
	if match.Route.UpstreamTarget != "second" {
		t.Fatalf("Match(second) selected upstream %q, want second", match.Route.UpstreamTarget)
	}
}

func TestThreadSafeTableHolderMultipleSwaps(t *testing.T) {
	holder := NewThreadSafeTableHolder(newHolderTestTable("target-0"))

	for i := 1; i <= 100; i++ {
		target := fmt.Sprintf("target-%d", i)
		table := newHolderTestTable(target)
		if err := holder.Swap(table); err != nil {
			t.Fatalf("Swap(%q) returned error: %v", target, err)
		}

		match, err := holder.Match(httptest.NewRequest("GET", "http://seam.test/items/42", nil))
		if err != nil {
			t.Fatalf("Match after Swap(%q) returned error: %v", target, err)
		}
		if got := match.Route.UpstreamTarget; got != target {
			t.Fatalf("Match after Swap(%q) selected %q", target, got)
		}
	}
}

func TestThreadSafeTableHolderConcurrentReadsDuringSwap(t *testing.T) {
	first := newHolderTestTable("first")
	second := newHolderTestTable("second")
	holder := NewThreadSafeTableHolder(first)

	const readers = 8
	const readsPerReader = 2_000
	const swaps = 1_000
	var wg sync.WaitGroup
	errors := make(chan string, 1)

	recordError := func(format string, args ...any) {
		select {
		case errors <- fmt.Sprintf(format, args...):
		default:
		}
	}

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "http://seam.test/items/42", nil)
			for j := 0; j < readsPerReader; j++ {
				match, err := holder.Match(req)
				if err != nil {
					recordError("concurrent Match returned error: %v", err)
					return
				}
				if match.Route.UpstreamTarget != "first" && match.Route.UpstreamTarget != "second" {
					recordError("concurrent Match observed partial route: %#v", match.Route)
					return
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < swaps; i++ {
			table := first
			if i%2 == 1 {
				table = second
			}
			if err := holder.Swap(table); err != nil {
				recordError("concurrent Swap returned error: %v", err)
				return
			}
		}
	}()

	wg.Wait()
	select {
	case message := <-errors:
		t.Fatal(message)
	default:
	}
}

func newHolderTestTable(upstream string) *RouteTable {
	table := NewRouteTable(nil)
	table.AddRoute(RouteEntry{
		PathTemplate:   "/items/{id}",
		Method:         "GET",
		APIVersion:     "v1",
		UpstreamTarget: upstream,
	})
	return table
}
