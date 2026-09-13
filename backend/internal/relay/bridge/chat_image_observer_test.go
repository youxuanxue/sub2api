package bridge

import "testing"

func TestCountChatImageOutputs(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"json", `{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,abc"}}]}}]}`, 1},
		{"text", `{"choices":[{"message":{"content":"hello"}}]}`, 0},
		{"sse dedupe", "data: {\"x\":\"data:image/png;base64,abc\"}\ndata: {\"x\":\"data:image/png;base64,abc\"}\n", 1},
		{"two", `{"a":"data:image/png;base64,a","b":"data:image/png;base64,b"}`, 2},
		{"identical two", `{"a":"data:image/png;base64,a","b":"data:image/png;base64,a"}`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countChatImageOutputs([]byte(tc.body)); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
