package openai

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeImageEditReferences maps alternate JSON image-edit shapes onto the
// images[].image_url array the edit paths read. Clients such as omp send
// input_references[] ({type:"image_url", url}) and some send a single image
// (string or object). An existing images array is left untouched (9ooniq).
func normalizeImageEditReferences(rawJSON []byte) []byte {
	if gjson.GetBytes(rawJSON, "images").Exists() {
		return rawJSON
	}
	var urls []string
	collect := func(item gjson.Result) {
		if item.Type == gjson.String {
			if url := strings.TrimSpace(item.String()); url != "" {
				urls = append(urls, url)
			}
			return
		}
		for _, path := range []string{"url", "image_url.url", "image_url"} {
			value := item.Get(path)
			if value.Type == gjson.String {
				if url := strings.TrimSpace(value.String()); url != "" {
					urls = append(urls, url)
					return
				}
			}
		}
	}
	if refs := gjson.GetBytes(rawJSON, "input_references"); refs.IsArray() {
		for _, item := range refs.Array() {
			collect(item)
		}
	}
	if len(urls) == 0 {
		if image := gjson.GetBytes(rawJSON, "image"); image.Exists() {
			collect(image)
		}
	}
	if len(urls) == 0 {
		return rawJSON
	}
	out := rawJSON
	for i, url := range urls {
		next, err := sjson.SetBytes(out, "images."+strconv.Itoa(i)+".image_url", url)
		if err != nil {
			return rawJSON
		}
		out = next
	}
	return out
}
