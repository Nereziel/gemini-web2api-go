package app

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func TestImageGenerationPromptSizes(t *testing.T) {
    cases := []struct {
        size string
        want string
        valid bool
    }{
        {"", "portrait", true},
        {"auto", "portrait", true},
        {"1024x1024", "1:1", true},
        {"1536x1024", "3:2", true},
        {"1024x1536", "2:3", true},
        {"256x256", "", false},
    }
    for _, tc := range cases {
        got, err := imageGenerationPrompt("portrait", tc.size)
        if (err == nil) != tc.valid {
            t.Errorf("%q: err=%v; valid=%v", tc.size, err, tc.valid)
        }
        if tc.valid && !strings.Contains(got, tc.want) {
            t.Errorf("%q: prompt=%q, want %q", tc.size, got, tc.want)
        }
    }
}

func TestImageGenerationsValidatesWithoutCallingUpstream(t *testing.T) {
    cases := []struct {
        name string
        method string
        body string
        status int
    }{
        {"method", http.MethodGet, "", 405},
        {"invalid json", http.MethodPost, "{", 400},
        {"empty prompt", http.MethodPost, `{"prompt":" "}`, 400},
        {"multiple images", http.MethodPost, `{"prompt":"cat","n":2}`, 400},
        {"url format", http.MethodPost, `{"prompt":"cat","response_format":"url"}`, 400},
        {"unsupported size", http.MethodPost, `{"prompt":"cat","size":"256x256"}`, 400},
        {"unsupported model", http.MethodPost, `{"prompt":"cat","model":"not-a-model"}`, 400},
        {"trailing json", http.MethodPost, `{"prompt":"cat"} {}`, 400},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            r := httptest.NewRequest(tc.method, "/v1/images/generations", strings.NewReader(tc.body))
            w := httptest.NewRecorder()
            handleImageGenerations(w, r)
            if w.Code != tc.status {
                t.Fatalf("status=%d, want %d: %s", w.Code, tc.status, w.Body.String())
            }
            var result map[string]interface{}
            if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
                t.Fatal(err)
            }
            if result["error"] == nil {
                t.Fatalf("missing OpenAI-style error: %s", w.Body.String())
            }
        })
    }
}
