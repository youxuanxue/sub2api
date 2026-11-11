//go:build unit

package kiro

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// These two small oracles preserve the former full-serialization loops. They
// deliberately use payloadByteSize rather than the optimized size accounting.
func referenceStripHistoryImages(p *KiroPayload, priming bool) {
	start := 0
	if priming && len(p.ConversationState.History) >= 2 {
		start = 2
	}
	for payloadByteSize(p) > maxPayloadBytes {
		removed := false
		for i := start; i < len(p.ConversationState.History); i++ {
			u := p.ConversationState.History[i].UserInputMessage
			if u == nil || len(u.Images) == 0 {
				continue
			}
			u.Images = u.Images[1:]
			if len(u.Images) == 0 {
				u.Images = nil
			}
			noteImageOmission(u)
			removed = true
			break
		}
		if !removed {
			return
		}
	}
}

func referenceDropCurrentImages(p *KiroPayload) {
	u := &p.ConversationState.CurrentMessage.UserInputMessage
	dropped := false
	for payloadByteSize(p) > maxPayloadBytes && len(u.Images) > 0 {
		u.Images = u.Images[1:]
		dropped = true
	}
	if dropped {
		if len(u.Images) == 0 {
			u.Images = nil
		}
		noteImageOmission(u)
	}
}

func imageSizeFixture(history, images, bytes int) *KiroPayload {
	p := &KiroPayload{ProfileArn: "test-profile"}
	p.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: "current", Origin: "AI_EDITOR"}
	for h := 0; h < history; h++ {
		u := &KiroUserInputMessage{Content: fmt.Sprintf("turn %d", h), Origin: "AI_EDITOR"}
		for i := 0; i < images; i++ {
			im := KiroImage{Format: "png"}
			im.Source.Bytes = strings.Repeat("A", bytes) + fmt.Sprintf("%d-%d", h, i)
			u.Images = append(u.Images, im)
		}
		p.ConversationState.History = append(p.ConversationState.History, KiroHistoryMessage{UserInputMessage: u})
	}
	return p
}

func padImageFixture(p *KiroPayload, target int) {
	if padding := target - payloadByteSize(p); padding > 0 {
		p.ConversationState.CurrentMessage.UserInputMessage.Content += strings.Repeat("x", padding)
	}
}

func TestImageReductionMatchesFullSerialization(t *testing.T) {
	unsupported := make(chan int)
	type fixture struct {
		name    string
		priming bool
		build   func() *KiroPayload
	}
	cases := []fixture{
		{"nil-history", false, func() *KiroPayload { return &KiroPayload{} }},
		{"no-images-oversized", false, func() *KiroPayload { p := imageSizeFixture(2, 0, 0); padImageFixture(p, maxPayloadBytes+1); return p }},
		{"multiple-images-and-turns", false, func() *KiroPayload { return imageSizeFixture(6, 4, 64*1024) }},
		{"priming-pair-protected", true, func() *KiroPayload { return imageSizeFixture(4, 3, 128*1024) }},
		{"only-priming-pair", true, func() *KiroPayload { return imageSizeFixture(2, 2, 256*1024) }},
		{"short-priming-history", true, func() *KiroPayload { return imageSizeFixture(1, 4, 256*1024) }},
		{"shared-history-user", false, func() *KiroPayload {
			p := imageSizeFixture(1, 4, 128*1024)
			p.ConversationState.History = append(p.ConversationState.History, p.ConversationState.History[0])
			return p
		}},
		{"history-shares-current-user", false, func() *KiroPayload {
			p := imageSizeFixture(1, 4, 128*1024)
			p.ConversationState.CurrentMessage.UserInputMessage = *p.ConversationState.History[0].UserInputMessage
			p.ConversationState.History[0].UserInputMessage = &p.ConversationState.CurrentMessage.UserInputMessage
			return p
		}},
		{"marshal-error-does-not-trim", false, func() *KiroPayload {
			p := imageSizeFixture(2, 4, 128*1024)
			p.ConversationState.History = append(p.ConversationState.History, KiroHistoryMessage{AssistantResponseMessage: &KiroAssistantResponseMessage{ToolUses: []KiroToolUse{{Input: map[string]interface{}{"unsupported": unsupported}}}}})
			return p
		}},
	}
	for _, offset := range []int{-1, 0, 1} {
		offset := offset
		cases = append(cases, fixture{fmt.Sprintf("initial-limit%+d", offset), false, func() *KiroPayload {
			p := imageSizeFixture(1, 2, 1024)
			padImageFixture(p, maxPayloadBytes+offset)
			return p
		}})
		cases = append(cases, fixture{fmt.Sprintf("after-one-removal%+d", offset), false, func() *KiroPayload {
			p := imageSizeFixture(1, 2, 1024)
			u := p.ConversationState.History[0].UserInputMessage
			all, content := u.Images, u.Content
			u.Images = u.Images[1:]
			noteImageOmission(u)
			padImageFixture(p, maxPayloadBytes+offset)
			u.Images, u.Content = all, content
			return p
		}})
		cases = append(cases, fixture{fmt.Sprintf("after-final-image%+d", offset), false, func() *KiroPayload {
			p := imageSizeFixture(1, 1, 1024)
			u := p.ConversationState.History[0].UserInputMessage
			all, content := u.Images, u.Content
			u.Images = nil
			noteImageOmission(u)
			padImageFixture(p, maxPayloadBytes+offset)
			u.Images, u.Content = all, content
			return p
		}})
		cases = append(cases, fixture{fmt.Sprintf("current-after-one-removal%+d", offset), false, func() *KiroPayload {
			p := imageSizeFixture(1, 2, 1024)
			u := &p.ConversationState.CurrentMessage.UserInputMessage
			u.Images = p.ConversationState.History[0].UserInputMessage.Images
			p.ConversationState.History = nil
			all := u.Images
			u.Images = u.Images[1:]
			padImageFixture(p, maxPayloadBytes+offset)
			u.Images = all
			return p
		}})
	}
	for i, content := range []string{"", " \n\t", "text\n\n", historyImagePlaceholder, "before " + historyImagePlaceholder + " after", "中文😀<>&\"\\\u2028\u2029\xff"} {
		content := content
		cases = append(cases, fixture{fmt.Sprintf("omission-and-escaping-%d", i), false, func() *KiroPayload {
			p := imageSizeFixture(1, 3, 0)
			u := p.ConversationState.History[0].UserInputMessage
			u.Content = content
			for j := range u.Images {
				u.Images[j].Format = "<png>\"\\\xff"
				u.Images[j].Source.Bytes = strings.Repeat("中文😀<>&\"\\\u2028\u2029\xff", 10000)
			}
			p.ConversationState.CurrentMessage.UserInputMessage.Images = append([]KiroImage(nil), u.Images...)
			p.ConversationState.CurrentMessage.UserInputMessage.Content = content
			return p
		}})
	}
	for seed := int64(0); seed < 32; seed++ {
		seed := seed
		cases = append(cases, fixture{fmt.Sprintf("seed-%d", seed), seed%2 == 0, func() *KiroPayload {
			r := rand.New(rand.NewSource(seed))
			p := imageSizeFixture(1+r.Intn(7), 1+r.Intn(5), 1024+r.Intn(48*1024))
			p.ConversationState.CurrentMessage.UserInputMessage.Images = append([]KiroImage(nil), p.ConversationState.History[0].UserInputMessage.Images...)
			padImageFixture(p, maxPayloadBytes-1000+r.Intn(2001))
			return p
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"history", "current", "history-then-current"} {
				t.Run(mode, func(t *testing.T) {
					got, want := tc.build(), tc.build()
					if mode != "current" {
						stripOldestHistoryImagesUntilFit(got, tc.priming)
						referenceStripHistoryImages(want, tc.priming)
					}
					if mode != "history" {
						dropOldestCurrentImagesUntilFit(got)
						referenceDropCurrentImages(want)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatal("reduction differs from full serialization, including nil versus empty slices")
					}
					gotJSON, gotErr := json.Marshal(got)
					wantJSON, wantErr := json.Marshal(want)
					if gotErr != nil || wantErr != nil {
						if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
							t.Fatal("marshal error changed image reduction behavior")
						}
						return
					}
					if string(gotJSON) != string(wantJSON) {
						t.Fatalf("reduction differs from full serialization: got size %d, want size %d", len(gotJSON), len(wantJSON))
					}
				})
			}
		})
	}
}

func BenchmarkHistoryImageReduction(b *testing.B) {
	for _, tc := range []struct {
		name                   string
		history, images, bytes int
	}{
		{"already-fits", 4, 1, 1024},
		{"many-small-images", 20, 5, 16 * 1024},
		{"few-large-images", 10, 1, 256 * 1024},
		{"long-history", 500, 1, 2048},
	} {
		for _, reference := range []bool{false, true} {
			name := "delta"
			if reference {
				name = "full-marshal"
			}
			b.Run(tc.name+"/"+name, func(b *testing.B) {
				p := imageSizeFixture(tc.history, tc.images, tc.bytes)
				images := make([][]KiroImage, tc.history)
				contents := make([]string, tc.history)
				for i, entry := range p.ConversationState.History {
					images[i], contents[i] = entry.UserInputMessage.Images, entry.UserInputMessage.Content
				}
				b.ReportAllocs()
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					for i, entry := range p.ConversationState.History {
						entry.UserInputMessage.Images, entry.UserInputMessage.Content = images[i], contents[i]
					}
					if reference {
						referenceStripHistoryImages(p, false)
					} else {
						stripOldestHistoryImagesUntilFit(p, false)
					}
				}
			})
		}
	}
}
