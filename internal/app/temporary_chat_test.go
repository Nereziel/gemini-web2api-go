package app

import (
	"encoding/json"
	"testing"
)

func TestTemporaryChatEligible(t *testing.T) {
	cases := []struct {
		name                     string
		tool                     int
		temp, multi, image, auth bool
		want                     bool
	}{
		{"text all off", 0, false, false, false, true, false},
		{"text global on", 0, true, false, false, true, true},
		{"text multi-turn", 0, true, true, true, true, false},
		{"text image setting only", 0, false, false, true, true, false},
		{"anonymous global on", 0, true, false, true, false, false},
		{"image default", toolImage, false, false, true, true, true},
		{"image with multi-turn", toolImage, false, true, true, true, true},
		{"image anonymous", toolImage, true, false, true, false, false},
		{"image disabled", toolImage, false, false, false, true, false},
		{"image global on", toolImage, true, false, false, true, true},
		{"image global blocked by multi-turn", toolImage, true, true, false, true, false},
		{"video unchanged", toolVideo, false, false, true, true, false},
		{"music unchanged", toolMusic, false, false, true, true, false},
		{"video global on", toolVideo, true, false, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := RuntimeConfig{TemporaryChat: c.temp, MultiTurn: c.multi, ImageTemporaryChat: c.image}
			if got := temporaryChatEligible(rt, ModelConfig{Tool: c.tool}, c.auth); got != c.want {
				t.Errorf("temporary chat = %v, want %v", got, c.want)
			}
		})
	}
}

func TestImageTemporaryChatDefaultAndExistingConfig(t *testing.T) {
	c := defaultConfig()
	if !c.ImageTemporaryChat {
		t.Fatal("image temporary chat must default to enabled")
	}
	if err := json.Unmarshal([]byte(`{"multi_turn":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.ImageTemporaryChat {
		t.Fatal("existing config without new field must retain the default")
	}
	rt := RuntimeConfig{ImageTemporaryChat: c.ImageTemporaryChat}
	if err := json.Unmarshal([]byte(`{"multi_turn":true}`), &rt); err != nil {
		t.Fatal(err)
	}
	if !temporaryChatEligible(rt, ModelConfig{Tool: toolImage}, true) {
		t.Fatal("saved runtime config without new field must retain image temporary chat")
	}
	if err := json.Unmarshal([]byte(`{"image_temporary_chat":false}`), &rt); err != nil {
		t.Fatal(err)
	}
	if temporaryChatEligible(rt, ModelConfig{Tool: toolImage}, true) {
		t.Fatal("explicitly disabling image temporary chat must be respected")
	}
}
