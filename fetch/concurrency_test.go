package fetch_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/slice"
)

// A fetch from a host keeps several range requests in flight -- no more
// than it is allowed, acquire.DefaultRequests unless told otherwise -- and
// one told to read a range at a time does: measured at a server whose every
// range takes a while to answer, as a host across the world does.
func TestFetchKeepsRequestsInFlight(t *testing.T) {
	path := writeDeepArchive(t, 3)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		requests, want int
	}{
		{0, acquire.DefaultRequests},
		{2, 2},
		{1, 1},
	} {
		var mu sync.Mutex
		inFlight, most := 0, 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			inFlight++
			most = max(most, inFlight)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			http.ServeContent(w, r, "a.pmtiles", time.Time{}, bytesReader(data))
			mu.Lock()
			inFlight--
			mu.Unlock()
		}))
		a, err := fetch.Open(srv.URL+"/a.pmtiles", fetch.Options{Requests: c.requests})
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		var planned *acquire.Plan
		res, err := fetch.Fill(context.Background(), filepath.Join(t.TempDir(), "store"), a, "(c) test",
			acquire.Request{Bounds: slice.Bounds{West: -170, South: -80, East: 170, North: 80}, MaxZoom: 3,
				Limits: acquire.Limits{MaxRequest: 64, MaxGap: -1}},
			func(p *acquire.Plan) { planned = p }, nil)
		a.Close()
		srv.Close()
		if err != nil {
			t.Fatalf("requests %d: %v", c.requests, err)
		}
		if planned == nil || planned.Requests < 2*acquire.DefaultRequests {
			t.Fatalf("precondition: want a plan of many requests, got %+v", planned)
		}
		if res.Written != planned.Tiles {
			t.Errorf("requests %d: wrote %d tiles of the %d planned", c.requests, res.Written, planned.Tiles)
		}
		if most != c.want {
			t.Errorf("requests %d: at most %d requests in flight, want %d", c.requests, most, c.want)
		}
	}
}

// bytesReader serves data afresh to each request: http.ServeContent seeks.
func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }
