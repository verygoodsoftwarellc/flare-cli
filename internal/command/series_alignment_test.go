package command

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

func TestSeriesCommandRejectsMisalignedArrays(t *testing.T) {
	t.Setenv("FLARE_TOKEN", "flare_pat_test")
	for _, test := range []struct {
		name    string
		buckets []string
		series  api.Series
	}{
		{"count", []string{"2026-11-08T00:00:00Z"}, api.Series{Count: nil, Sum: []int64{30}, ErrorCount: []int64{0}}},
		{"sum", []string{"2026-11-08T00:00:00Z"}, api.Series{Count: []int64{2}, Sum: nil, ErrorCount: []int64{0}}},
		{"error_count", []string{"2026-11-08T00:00:00Z"}, api.Series{Count: []int64{2}, Sum: []int64{30}, ErrorCount: nil}},
		{"extra_values", []string{"2026-11-08T00:00:00Z"}, api.Series{Count: []int64{2, 3}, Sum: []int64{30}, ErrorCount: []int64{0}}},
		{"no_buckets", nil, api.Series{Count: []int64{2}, Sum: []int64{30}, ErrorCount: []int64{0}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("command panicked: %v", v)
				}
			}()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(api.OperationSeriesResponse{SeriesEnvelope: api.SeriesEnvelope{Buckets: test.buckets, Series: test.series}})
			}))
			defer server.Close()
			output := &bytes.Buffer{}
			command := newSeriesCommand(&rootOptions{apiURL: server.URL, output: output, errors: io.Discard})
			command.SetArgs([]string{"--environment", "1", "--namespace", "web"})
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), "invalid series response") {
				t.Fatalf("expected alignment error,got %v", err)
			}
			if output.Len() != 0 {
				t.Errorf("invalid response printed partial output: %s", output)
			}
		})
	}
}

func TestSeriesCommandKeepsValidAndRawOutput(t *testing.T) {
	t.Setenv("FLARE_TOKEN", "flare_pat_test")
	for _, test := range []struct {
		name   string
		json   bool
		series api.Series
		want   string
	}{
		{"aligned", false, api.Series{Count: []int64{2}, Sum: []int64{30}, ErrorCount: []int64{0}}, "15ms"},
		{"raw", true, api.Series{}, "buckets"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(api.OperationSeriesResponse{SeriesEnvelope: api.SeriesEnvelope{Buckets: []string{"2026-11-08T00:00:00Z"}, Series: test.series}})
			}))
			defer server.Close()
			output := &bytes.Buffer{}
			command := newSeriesCommand(&rootOptions{apiURL: server.URL, json: test.json, output: output, errors: io.Discard})
			command.SetArgs([]string{"--environment", "1", "--namespace", "web"})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("missing %q in %q", test.want, output)
			}
		})
	}
}
