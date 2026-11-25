# OpenAI-compatible image generation

`POST /v1/images/generations` uses the existing Gemini Web `gemini-image`
tool (Nano Banana), **not OpenAI's image-generation service**.

Requires a signed-in Gemini cookie and the same Bearer API key as other
`/v1/*` routes.

```bash
curl -sS http://localhost:8083/v1/images/generations \
  -H 'Authorization: Bearer YOUR_API_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-image","prompt":"A fantasy dwarf portrait","size":"1024x1024","n":1,"response_format":"b64_json"}'
```

Success response: `{"created": 1234567890, "data":[{"b64_json":"..."}]}`.
Decode `data[0].b64_json` into an image; its actual MIME type and pixel
dimensions depend on Gemini's output.

## Temporary chat

Standalone image generation uses Gemini's native Temporary Chat by default
(`image_temporary_chat: true`), including requests through this endpoint.
It is independent of text multi-turn. Disable **Temporary chat for images**
in the admin runtime settings or set `image_temporary_chat` to `false` to
return to the global `temporary_chat` / `auto_delete_conversation` behavior.
The image bytes are fetched before returning the result. Temporary requests
skip the separate auto-delete operation. Upstream image retrieval still
depends on Gemini accepting the request and exposing the image download.

Compatibility:
- `model`: `gemini-image` (recommended), plus client compatibility aliases
  `gpt-image-1`, `gpt-image-1-mini`, `dall-e-3` (all use Gemini).
- `n`: only 1.
- `response_format`: only `b64_json` (default).
- `size`: `auto`, `1024x1024`, `1536x1024`, `1024x1536`.
  Size is translated to an aspect-ratio prompt **hint**, not exact resizing.
- Image edits, masks, URL responses, guaranteed output format/size, and
  provider-specific quality/background controls are not implemented.

A Gemini Web generation may succeed but the associated image download may
fail (including HTTP 403). The API returns an error rather than an empty
successful image response. This route reuses the existing download handling;
it does **not** by itself fix upstream cookie/CDN authorization failures.
