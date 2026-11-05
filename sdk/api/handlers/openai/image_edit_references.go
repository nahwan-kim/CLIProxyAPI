package openai

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// normalizeImageEditReferences maps alternate JSON image-edit shapes onto the
// images[].image_url array the edit paths read. Clients such as omp send
// input_references[] ({type:"image_url", url}) and some send a single image
// (string or object). An existing images array is left untouched (9ooniq).
func normalizeImageEditReferences(rawJSON []byte) []byte {
	if ojson.GetBytes(rawJSON, "images").Exists() {
		return rawJSON
	}
	var urls []string
	collect := func(item ojson.Result) {
		if item.Type == ojson.String {
			if url := strings.TrimSpace(item.String()); url != "" {
				urls = append(urls, url)
			}
			return
		}
		for _, path := range []string{"url", "image_url.url", "image_url"} {
			value := item.Get(path)
			if value.Type == ojson.String {
				if url := strings.TrimSpace(value.String()); url != "" {
					urls = append(urls, url)
					return
				}
			}
		}
	}
	if refs := ojson.GetBytes(rawJSON, "input_references"); refs.IsArray() {
		for _, item := range refs.Array() {
			collect(item)
		}
	}
	if len(urls) == 0 {
		if image := ojson.GetBytes(rawJSON, "image"); image.Exists() {
			collect(image)
		}
	}
	if len(urls) == 0 {
		return rawJSON
	}
	doc := ojson.NewDoc(rawJSON)
	for i, url := range urls {
		if err := doc.Set("images."+strconv.Itoa(i)+".image_url", url); err != nil {
			return rawJSON
		}
	}
	return doc.Bytes()
}
