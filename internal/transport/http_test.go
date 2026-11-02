package transport

import (
	"bufio"
	"encoding/json"
	"github.com/ponytojas/darsena/internal/modules/demo"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestBoundaries(t *testing.T) {
	h := API{Store: demo.New(time.Second)}.Handler("")
	for _, tc := range []struct {
		name, host, origin, body string
		status                   int
	}{{"foreign host", "evil.test", "", "", 403}, {"foreign origin", "localhost:7300", "https://evil.test", "", 403}, {"unknown field", "localhost:7300", "", `{"command":"rm"}`, 400}, {"extra object", "localhost:7300", "", `{} {}`, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+tc.host+"/api/v1/applications", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestSSEReconnectGetsCurrentSnapshot(t *testing.T) {
	s := demo.New(time.Second)
	_, o, err := s.Create(demo.CreateInput{Name: "test-app", Repository: "demo/test", Branch: "main", Context: ".", Port: 3000, Scenario: "success"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(API{Store: s}.Handler(""))
	defer server.Close()
	for _, cursor := range []string{"", "999"} {
		req, _ := http.NewRequest("GET", server.URL+"/api/v1/operations/"+o.ID+"/events", nil)
		req.Header.Set("Last-Event-ID", cursor)
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(resp.Body)
		found := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var got demo.Operation
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &got); err != nil {
					t.Fatal(err)
				}
				if got.ID != o.ID || got.Revision != o.Revision {
					t.Fatal("wrong snapshot")
				}
				found = true
				break
			}
		}
		_ = resp.Body.Close()
		if !found {
			t.Fatal("missing snapshot")
		}
	}
}
func TestLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7300", ":7300", "192.168.1.1:7300", "localhost:7300"} {
		if LoopbackAddress(addr) {
			t.Fatal(addr)
		}
	}
	if !LoopbackAddress("127.0.0.1:7300") || !LoopbackAddress("[::1]:7300") {
		t.Fatal("loopback rejected")
	}
}
func TestProblemCodes(t *testing.T) {
	h := API{Store: demo.New(time.Second)}.Handler("")
	for _, tc := range []struct {
		name, method, path, body, code string
		status                         int
	}{
		{"validation", "POST", "/api/v1/applications", `{"name":"X","repository":"a/b","branch":"main","context":".","port":3000,"scenario":"success"}`, "invalid_name", 400},
		{"unknown field", "POST", "/api/v1/applications", `{"command":"rm"}`, "invalid_json", 400},
		{"app missing", "GET", "/api/v1/applications/nope", "", "application_not_found", 404},
		{"operation missing", "GET", "/api/v1/operations/nope", "", "operation_not_found", 404},
		{"deploy missing", "POST", "/api/v1/applications/nope/deployments", `{"scenario":"success"}`, "application_not_found", 404},
		{"bad scenario", "POST", "/api/v1/applications/bitacora/deployments", `{"scenario":"x"}`, "invalid_scenario", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://localhost:7300"+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var got struct{ Error, Code string }
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != tc.status || got.Code != tc.code || got.Error == "" {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
