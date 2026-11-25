package app

import (
    "encoding/base64"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "strings"
    "time"
)

// imageGenerationRequest is deliberately limited to the subset the Gemini Web
// backend can actually provide. A requested size is a prompt hint, not a guarantee.
type imageGenerationRequest struct {
    Model string `json:"model"`
    Prompt string `json:"prompt"`
    N *int `json:"n"`
    Size string `json:"size"`
    ResponseFormat string `json:"response_format"`
    User string `json:"user"`
}

func imageGenerationError(w http.ResponseWriter, status int, msg, kind string) {
    writeJSON(w, status, map[string]interface{}{"error": map[string]string{
        "message": msg, "type": kind,
    }})
}

func imageGenerationPrompt(prompt, size string) (string, error) {
    switch size {
    case "", "auto":
        return prompt, nil
    case "1024x1024":
        return prompt + "\n\nImage composition: square (1:1 aspect ratio).", nil
    case "1536x1024":
        return prompt + "\n\nImage composition: landscape (3:2 aspect ratio).", nil
    case "1024x1536":
        return prompt + "\n\nImage composition: portrait (2:3 aspect ratio).", nil
    default:
        return "", fmt.Errorf("unsupported size %q; use auto, 1024x1024, 1536x1024 or 1024x1536", size)
    }
}

// handleImageGenerations implements OpenAI-compatible POST /v1/images/generations,
// using the existing Gemini Web image tool (not OpenAI's paid image models).
func handleImageGenerations(w http.ResponseWriter, r *http.Request) {
    if r.Method == http.MethodOptions {
        handleOptions(w, r)
        return
    }
    if r.Method != http.MethodPost {
        imageGenerationError(w, 405, "method not allowed", "invalid_request_error")
        return
    }
    r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
    defer r.Body.Close()
    var req imageGenerationRequest
    dec := json.NewDecoder(r.Body)
    if err := dec.Decode(&req); err != nil {
        imageGenerationError(w, 400, "invalid request JSON: "+err.Error(), "invalid_request_error")
        return
    }
    if err := dec.Decode(new(interface{})); err != io.EOF {
        imageGenerationError(w, 400, "request body must contain a single JSON object", "invalid_request_error")
        return
    }
    if strings.TrimSpace(req.Prompt) == "" {
        imageGenerationError(w, 400, "prompt is required", "invalid_request_error")
        return
    }
    if req.N != nil && *req.N != 1 {
        imageGenerationError(w, 400, "only n=1 is supported by the Gemini Web image backend", "invalid_request_error")
        return
    }
    if req.ResponseFormat != "" && req.ResponseFormat != "b64_json" {
        imageGenerationError(w, 400, "only response_format=b64_json is supported", "invalid_request_error")
        return
    }
    // Aliases permit existing OpenAI image clients to use this endpoint. They
    // do not change the actual provider or guarantee OpenAI model behavior.
    switch req.Model {
    case "", "gemini-image", "gpt-image-1", "gpt-image-1-mini", "dall-e-3":
    default:
        imageGenerationError(w, 400, "unsupported image model: "+req.Model, "invalid_request_error")
        return
    }
    prompt, err := imageGenerationPrompt(req.Prompt, req.Size)
    if err != nil {
        imageGenerationError(w, 400, err.Error(), "invalid_request_error")
        return
    }
    modelName, mc, err := resolveModel("gemini-image")
    if err != nil {
        imageGenerationError(w, 400, err.Error(), "invalid_request_error")
        return
    }

    res, err := streamGenerateWithFiles(prompt, prompt, mc, nil, nil, nil)
    if err == nil && (res == nil || len(res.Artifacts) == 0) {
        err = errors.New("image generation returned no image artifact")
        if res != nil && res.MediaErr != nil {
            err = fmt.Errorf("image artifact download failed: %w", res.MediaErr)
        }
    }
    if err != nil {
        var imageLimit *ImageRateLimitError
        if errors.As(err, &imageLimit) {
            recordRequest("images.generations", modelName, req.Prompt, "", res, 429, err.Error(), false)
            writeImageRateLimitError(w, imageLimit)
            return
        }
        status := 502
        var rateErr *RateLimitError
        var longErr *PromptTooLongError
        var downgraded *ModelDowngradedError
        switch {
        case errors.As(err, &rateErr), errors.As(err, &downgraded):
            status = 429
        case errors.As(err, &longErr):
            status = 400
        }
        recordRequest("images.generations", modelName, req.Prompt, "", res, status, err.Error(), false)
        kind := "upstream_error"
        if status == 429 { kind = "rate_limit_error" }
        if status == 400 { kind = "invalid_request_error" }
        imageGenerationError(w, status, err.Error(), kind)
        return
    }

    images := make([]map[string]string, 0, len(res.Artifacts))
    for _, artifact := range res.Artifacts {
        if !strings.HasPrefix(artifact.Mime, "image/") || len(artifact.Data) == 0 ||
            !strings.HasPrefix(http.DetectContentType(artifact.Data), "image/") {
            recordRequest("images.generations", modelName, req.Prompt, "", res, 502, "invalid image artifact", false)
            imageGenerationError(w, 502, "upstream returned an invalid image artifact", "upstream_error")
            return
        }
        images = append(images, map[string]string{
            "b64_json": base64.StdEncoding.EncodeToString(artifact.Data),
        })
    }
    recordRequest("images.generations", modelName, req.Prompt, "", res, 200, "", false)
    writeJSON(w, 200, map[string]interface{}{
        "created": time.Now().Unix(),
        "data": images,
    })
}
