package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClockCLIUsesBoundedNodeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/guest/clock" || r.URL.Query().Get("sandbox") != strings.Repeat("a", 64) || r.URL.Query().Get("samples") != "3" {
			t.Error(r.URL)
		}
		_, _ = w.Write([]byte(`{"offsetLowerNs":"-9007199254740993"}`))
	}))
	defer server.Close()
	endpoint := server.URL
	cmd := newClockCommand(&endpoint)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--sandbox", strings.Repeat("a", 64), "--samples", "3"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"-9007199254740993"`) {
		t.Fatal("lost wire integer precision", out.String())
	}
	for _, args := range [][]string{{"--sandbox", "x", "--samples", "2"}, {"--samples", "3"}, {"--sandbox", "x", "extra"}} {
		cmd = newClockCommand(&endpoint)
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err == nil {
			t.Fatal("invalid CLI accepted", args)
		}
	}
}
