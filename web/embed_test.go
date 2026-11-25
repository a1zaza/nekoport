package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticRoutesDoNotMaskMissingAPIsOrAssets(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/api/v1/does-not-exist", "/assets/missing.js", "/private/data.json"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if Check() != nil {
		t.Skip("前端未构建，仅检查资源路由边界")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "nekoport") {
		t.Fatal("embedded production frontend missing")
	}
}
