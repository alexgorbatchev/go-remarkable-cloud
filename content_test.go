package cloud_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

const contentFixture = `{
 "fileType":"pdf","formatVersion":2,"pageCount":2,"coverPageNumber":0,
 "cPages":{
  "original":{"timestamp":"1:1","value":2,"future":9007199254740993},
  "lastOpened":{"timestamp":"1:2","value":"page-b"},
  "uuids":[{"first":"page-a","second":"page-b"}],
  "pages":[
   {"id":"page-a","idx":{"timestamp":"1:3","value":"ba"},
    "redir":{"timestamp":"1:4","value":0},
    "template":{"timestamp":"1:5","value":"Blank"},
    "modifed":"1:6","scrollTime":{"timestamp":"1:7","value":15},
    "verticalScroll":{"timestamp":"1:8","value":0.25}},
   {"id":"page-b","idx":{"timestamp":"1:9","value":"ab"},
    "deleted":{"timestamp":"1:10","value":1},"futurePage":{"list":[]}}
  ],"futureCollection":null
 },
 "customZoomCenterX":0,"customZoomCenterY":1.5,"customZoomOrientation":"portrait",
 "customZoomPageHeight":100,"customZoomPageWidth":80,"customZoomScale":1,
 "documentMetadata":{"title":"Example"},"dummyDocument":false,"extraMetadata":{},
 "fontName":"","keyboardMetadata":{"language":"en"},"lineHeight":1,
 "orientation":"portrait","pageTags":[],"sizeInBytes":"42","tags":[{"name":"Example"}],
 "textAlignment":"left","textScale":1,"viewBackgroundFilter":"full","zoomMode":"bestFit",
 "futureNumber":9007199254740993
}`

func jsonValue(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	// Keep numeric precision in the semantic comparison.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestContentRoundTrip(t *testing.T) {
	for _, input := range []string{contentFixture, `{}`, `null`, `{"cPages":null,"pageCount":null,"pages":[]}`, `{"cPages":{"pages":[],"original":null},"fileType":"","pageCount":0}`} {
		t.Run(input[:min(len(input), 30)], func(t *testing.T) {
			var content cloud.DocumentContent
			if err := json.Unmarshal([]byte(input), &content); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(jsonValue(t, []byte(input)), jsonValue(t, encoded)) {
				t.Fatalf("round trip changed content:\n%s", encoded)
			}
			if input == contentFixture {
				if content.CPages.Original.Value != 2 || content.CPages.Pages[0].Idx.Value != "ba" || content.CPages.Pages[0].Template.Value != "Blank" {
					t.Fatal("registers not decoded")
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &raw); err != nil {
					t.Fatal(err)
				}
				if string(raw["futureNumber"]) != "9007199254740993" {
					t.Fatal("unknown number lost precision")
				}
			}
		})
	}
}

func TestContentEditsPreserveUnchangedFields(t *testing.T) {
	var content cloud.DocumentContent
	if err := json.Unmarshal([]byte(contentFixture), &content); err != nil {
		t.Fatal(err)
	}
	content.PageCount = 0
	content.CPages.Pages[0].Idx.Value = "ac"
	content.CPages.Pages[0].Redir.Value = 1
	content.CPages.Pages[0].Extra["added"] = json.RawMessage(`{"value":true}`)
	delete(content.Extra, "tags")
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	expected := jsonValue(t, []byte(contentFixture)).(map[string]any)
	expected["pageCount"] = json.Number("0")
	delete(expected, "tags")
	page := expected["cPages"].(map[string]any)["pages"].([]any)[0].(map[string]any)
	page["idx"].(map[string]any)["value"] = "ac"
	page["redir"].(map[string]any)["value"] = json.Number("1")
	page["added"] = map[string]any{"value": true}
	if !reflect.DeepEqual(expected, jsonValue(t, encoded)) {
		t.Fatalf("edits lost unrelated data: %s", encoded)
	}
	if err := json.Unmarshal([]byte(`{}`), &content); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(content)
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("decode retained stale state: %s, %v", encoded, err)
	}
}

func TestContentRejectsInvalidKnownTypes(t *testing.T) {
	for _, input := range []string{`[]`, `{"pageCount":"two"}`, `{"cPages":[]}`, `{"cPages":{"pages":[false]}}`, `{"cPages":{"original":{"value":"two"}}}`} {
		var content cloud.DocumentContent
		if err := json.Unmarshal([]byte(input), &content); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestConstructedContentAndInvalidExtras(t *testing.T) {
	content := cloud.DocumentContent{FileType: "pdf", CPages: cloud.CPages{Pages: []cloud.CPage{{ID: "new", Redir: &cloud.Register[int]{Value: 0, Timestamp: "1:1"}}}}}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var decoded cloud.DocumentContent
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FileType != "pdf" || decoded.CPages.Pages[0].Redir.Timestamp != "1:1" {
		t.Fatal("constructed fields lost")
	}
	for _, extra := range []*map[string]json.RawMessage{&content.Extra, &content.CPages.Extra, &content.CPages.Pages[0].Extra, &content.CPages.Pages[0].Redir.Extra} {
		*extra = map[string]json.RawMessage{"bad": json.RawMessage(`{`)}
		if _, err := json.Marshal(content); err == nil {
			t.Fatal("invalid extra accepted")
		}
		*extra = nil
	}
}
