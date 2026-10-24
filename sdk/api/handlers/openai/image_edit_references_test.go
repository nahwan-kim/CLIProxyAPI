package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeImageEditReferences(t *testing.T) {
	cases := map[string]string{
		"input_references": `{"model":"gpt-image-2.5","input_references":[{"type":"image_url","url":"data:a"},{"type":"image_url","url":"data:b"}]}`,
		"image string":     `{"model":"gpt-image-2.5","image":"data:a"}`,
		"image object":     `{"model":"gpt-image-2.5","image":{"image_url":{"url":"data:a"}}}`,
	}
	for name, body := range cases {
		out := normalizeImageEditReferences([]byte(body))
		if got := gjson.GetBytes(out, "images.0.image_url").String(); got != "data:a" {
			t.Fatalf("%s: images.0.image_url = %q, body %s", name, got, out)
		}
	}
	out := normalizeImageEditReferences([]byte(cases["input_references"]))
	if got := gjson.GetBytes(out, "images.1.image_url").String(); got != "data:b" {
		t.Fatalf("second reference lost: %s", out)
	}
	keep := `{"images":[{"image_url":"data:x"}],"input_references":[{"url":"data:y"}]}`
	if string(normalizeImageEditReferences([]byte(keep))) != keep {
		t.Fatal("existing images array must be left untouched")
	}
	none := `{"model":"gpt-image-2.5","prompt":"x"}`
	if string(normalizeImageEditReferences([]byte(none))) != none {
		t.Fatal("body without references must be unchanged")
	}
}
