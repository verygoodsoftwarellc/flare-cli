package command

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOverviewListsDeploysLikeTheEnvironmentPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"overviews":[
			{"environment":{"id":1,"project_id":1,"name":"Production"},"changes":[
				{"id":3,"kind":"deploy","title":"Deploy","revision":"rel-2026-11-20-hotfix","previous_revision":"9f8e7d6c5b4a","rollback":false,"occurred_at":"2026-11-20T15:30:00Z","finished_at":null,"rollout_seconds":null,"bucket":"2026-11-20T15:00:00Z"},
				{"id":2,"kind":"deploy","title":"Deploy","revision":"9f8e7d6c5b4a39281706","previous_revision":"a1b2c3d4e5f6","rollback":true,"occurred_at":"2026-11-20T14:05:00Z","finished_at":"2026-11-20T14:07:05Z","rollout_seconds":125,"bucket":"2026-11-20T14:00:00Z"},
				{"id":1,"kind":"deploy","title":"Deploy","revision":"a1b2c3d4e5f6a7b8","previous_revision":null,"rollback":false,"occurred_at":"2026-11-20T12:00:00Z","finished_at":"2026-11-20T12:00:00Z","rollout_seconds":0,"bucket":"2026-11-20T12:00:00Z"}
			],"namespaces":[{"namespace":"web","impact":100,"count":10,"sum":500,"avg":50,"error_rate":0,"operations":[]}]},
			{"environment":{"id":2,"project_id":1,"name":"Staging"},"changes":[],"namespaces":[{"namespace":"web","impact":100,"count":1,"sum":5,"avg":5,"error_rate":0,"operations":[]}]}
		]}`))
	}))
	defer server.Close()
	t.Setenv("FLARE_TOKEN", "flare_pat_test")

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}}
	command := newOverviewCommand(options)
	command.SetArgs([]string{"-e", "1", "-e", "2"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	text := output.String()
	_, deploys, found := strings.Cut(text, "\nProduction: deploys\n")
	if !found {
		t.Fatalf("missing Production deploys section in:\n%s", text)
	}
	lines := strings.Split(strings.TrimRight(deploys, "\n"), "\n")
	want := [][]string{
		{"TIME", "REVISION", "ROLLOUT"},
		{"2026-11-20", "10:30", "EST", "rel-2026-11-", "rolling", "out"},
		{"2026-11-20", "09:05", "EST", "9f8e7d6", "rollback", "2m", "5s"},
		{"2026-11-20", "07:00", "EST", "a1b2c3d", "instant"},
	}
	if len(lines) != len(want) {
		t.Fatalf("want %d deploy lines, got:\n%s", len(want), deploys)
	}
	for index, fields := range want {
		if got := strings.Fields(lines[index]); strings.Join(got, " ") != strings.Join(fields, " ") {
			t.Errorf("line %d = %q, want fields %q", index, lines[index], fields)
		}
	}
	if strings.Contains(text, "Staging: deploys") {
		t.Fatalf("environment without deploys got a section:\n%s", text)
	}
}

func TestTablesAndHeadingsDropControlCharacters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"overviews":[{"environment":{"id":1,"project_id":1,"name":"Prod\u001b[2J\nuction"},
			"changes":[{"id":1,"kind":"deploy","title":"Deploy","revision":"rel\t1\u001b[0m\u009b","previous_revision":null,"rollback":false,"occurred_at":"2026-11-20T12:00:00Z","finished_at":null,"rollout_seconds":null,"bucket":null}],
			"namespaces":[{"namespace":"web","impact":100,"count":1,"sum":5,"avg":5,"error_rate":0,"operations":[{"operation":"Users\u001b[31m#index","impact":100,"count":1,"sum":5,"avg":5,"error_rate":0}]}]}]}`))
	}))
	defer server.Close()
	t.Setenv("FLARE_TOKEN", "flare_pat_test")

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}}
	command := newOverviewCommand(options)
	command.SetArgs([]string{"-e", "1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	text := output.String()
	if strings.ContainsAny(text, "\x1b\t\u009b") {
		t.Fatalf("control characters reached the output: %q", text)
	}
	for _, want := range []string{"\nProd[2Juction web: top operations by sum\n", "Users[31m#index", "\nProd[2Juction: deploys\n", "rel1[0m"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}
