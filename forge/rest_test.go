package forge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" {
			http.Error(w, `{"message":"no header"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"n":3}`))
	}))
	t.Cleanup(server.Close)

	var out struct{ N int }
	err := doJSON(t.Context(), nil, server.URL+"/", http.MethodGet, "/x", http.Header{"X-Test": {"yes"}}, nil, &out)
	if err != nil || out.N != 3 {
		t.Errorf("doJSON = %+v, %v", out, err)
	}
	err = doJSON(t.Context(), nil, server.URL, http.MethodGet, "/x", nil, nil, &out)
	if apiErr, ok := errors.AsType[*apiError](err); !ok || apiErr.Status != http.StatusBadRequest || !strings.Contains(err.Error(), "no header") {
		t.Errorf("doJSON without the header: err = %v", err)
	}
}

func TestAPIMessage(t *testing.T) {
	if got := apiMessage([]byte(`{"message":"boom"}`)); got != "boom" {
		t.Errorf("apiMessage = %q", got)
	}
	if got := apiMessage([]byte("  <html>  ")); got != `"<html>"` {
		t.Errorf("apiMessage of a non-JSON body = %q", got)
	}
}
