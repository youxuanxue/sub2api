package bridge

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

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

func TestChatImageObserver_Issue2161_LargeJSONOver8MiB(t *testing.T) {
	prefix := `{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`
	suffix := `"}}]}}]}`
	payloadLen := 8389737 - len(prefix) - len(suffix)
	payload := bytes.Repeat([]byte("A"), payloadLen)

	var fullBody bytes.Buffer
	_, _ = fullBody.WriteString(prefix)
	_, _ = fullBody.Write(payload)
	_, _ = fullBody.WriteString(suffix)

	require.Equal(t, 8389737, fullBody.Len())

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	observer := &chatImageObserver{ResponseWriter: c.Writer}
	c.Writer = observer

	n, err := observer.Write(fullBody.Bytes())
	require.NoError(t, err)
	require.Equal(t, 8389737, n)
	require.Equal(t, 8389737, rec.Body.Len(), "delivered bytes must be complete")

	require.Equal(t, 1, observer.imageCount(), "observer must not truncate or drop image count to 0")
	require.Less(t, observer.jsonBuf.Len(), 1024, "memory in observer must stay bounded")
}

func TestChatImageObserver_ChunkedWrites(t *testing.T) {
	prefix := `{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`
	suffix := `"}}]}}]}`
	payload := bytes.Repeat([]byte("B"), 8389737-len(prefix)-len(suffix))

	var fullBody bytes.Buffer
	_, _ = fullBody.WriteString(prefix)
	_, _ = fullBody.Write(payload)
	_, _ = fullBody.WriteString(suffix)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	observer := &chatImageObserver{ResponseWriter: c.Writer}

	chunkSize := 4096
	data := fullBody.Bytes()
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		n, err := observer.Write(data[i:end])
		require.NoError(t, err)
		require.Equal(t, end-i, n)
	}

	require.Equal(t, 8389737, rec.Body.Len())
	require.Equal(t, 1, observer.imageCount())
	require.Less(t, observer.jsonBuf.Len(), 1024)
}

func TestChatImageObserver_SplitsAcrossBoundaries(t *testing.T) {
	chunks := []string{
		`{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"dat`,
		`a:im`,
		`age/png;`,
		`base64,`,
		string(bytes.Repeat([]byte("A"), 10000)),
		string(bytes.Repeat([]byte("B"), 10000)),
		`"}}]}}]}`,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	observer := &chatImageObserver{ResponseWriter: c.Writer}

	for _, chunk := range chunks {
		_, err := observer.Write([]byte(chunk))
		require.NoError(t, err)
	}

	require.Equal(t, 1, observer.imageCount())
	require.Less(t, observer.jsonBuf.Len(), 1024)
}

func TestChatImageObserver_SSE_LargeAndDeduplication(t *testing.T) {
	largePayload := bytes.Repeat([]byte("C"), 8389737)
	event1 := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/png;base64,%s\"}}]}}]}\n\n", largePayload)
	event2 := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/png;base64,%s\"}}]}}]}\n\n", largePayload)
	event3 := "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/jpeg;base64,different\"}}]}}]}\n\n"
	event4 := "data: [DONE]\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	observer := &chatImageObserver{ResponseWriter: c.Writer}

	// Write in chunks
	chunks := [][]byte{
		[]byte(event1[:len(event1)/2]),
		[]byte(event1[len(event1)/2:]),
		[]byte(event2),
		[]byte(event3),
		[]byte(event4),
	}
	for _, chunk := range chunks {
		_, err := observer.Write(chunk)
		require.NoError(t, err)
	}

	require.Equal(t, 2, observer.imageCount(), "must deduplicate identical image across SSE events")
	require.Equal(t, 0, observer.lineBuf.Len(), "line buffer must be reset after processing lines")
}

func TestChatImageObserver_ErrorStatusNotCounted(t *testing.T) {
	body := `{"error":{"message":"bad request"},"url":"data:image/png;base64,AAAA"}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	observer := &chatImageObserver{ResponseWriter: c.Writer}

	observer.WriteHeader(400)
	_, err := observer.Write([]byte(body))
	require.NoError(t, err)

	require.Equal(t, 0, observer.imageCount(), "status >= 400 must not count images")
}

func TestChatImageObserver_MultiImageJSON(t *testing.T) {
	body := `{"choices":[{"message":{"content":[
		{"type":"image_url","image_url":{"url":"data:image/png;base64,one"}},
		{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,two"}},
		{"type":"image_url","image_url":{"url":"data:image/webp;base64,three"}},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,one"}}
	]}}]}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	observer := &chatImageObserver{ResponseWriter: c.Writer}
	_, err := observer.Write([]byte(body))
	require.NoError(t, err)

	require.Equal(t, 4, observer.imageCount(), "JSON must count each image occurrence")
}
