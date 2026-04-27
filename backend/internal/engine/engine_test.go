//go:build unit

package engine

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

func TestBuildDispatchPlan(t *testing.T) {
	cases := []struct {
		name string
		in   BridgeDispatchInput
		want string
	}{
		{
			name: "newapi chat bridge eligible uses newapi bridge",
			in: BridgeDispatchInput{
				AccountPlatform: domain.PlatformNewAPI,
				ChannelType:     45,
				Endpoint:        BridgeEndpointChatCompletions,
				BridgeEnabled:   true,
			},
			want: ProviderNewAPIBridge,
		},
		{
			name: "openai chat bridge eligible uses newapi bridge",
			in: BridgeDispatchInput{
				AccountPlatform: domain.PlatformOpenAI,
				ChannelType:     45,
				Endpoint:        BridgeEndpointChatCompletions,
				BridgeEnabled:   true,
			},
			want: ProviderNewAPIBridge,
		},
		{
			name: "bridge disabled uses native",
			in: BridgeDispatchInput{
				AccountPlatform: domain.PlatformNewAPI,
				ChannelType:     45,
				Endpoint:        BridgeEndpointChatCompletions,
				BridgeEnabled:   false,
			},
			want: ProviderNative,
		},
		{
			name: "unsupported scheduling platform uses native",
			in: BridgeDispatchInput{
				AccountPlatform: domain.PlatformAnthropic,
				ChannelType:     45,
				Endpoint:        BridgeEndpointChatCompletions,
				BridgeEnabled:   true,
			},
			want: ProviderNative,
		},
		{
			name: "channel type zero uses native when endpoint requires channel type",
			in: BridgeDispatchInput{
				AccountPlatform: domain.PlatformNewAPI,
				ChannelType:     0,
				Endpoint:        BridgeEndpointChatCompletions,
				BridgeEnabled:   true,
			},
			want: ProviderNative,
		},
		{
			name: "video endpoint without registered task adaptor uses native",
			in: BridgeDispatchInput{
				AccountPlatform: domain.PlatformNewAPI,
				ChannelType:     9999,
				Endpoint:        BridgeEndpointVideoSubmit,
				BridgeEnabled:   true,
			},
			want: ProviderNative,
		},
		{
			name: "unknown endpoint uses native",
			in: BridgeDispatchInput{
				AccountPlatform: domain.PlatformNewAPI,
				ChannelType:     45,
				Endpoint:        "unknown",
				BridgeEnabled:   true,
			},
			want: ProviderNative,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildDispatchPlan(tc.in)
			if got.Provider != tc.want {
				t.Fatalf("BuildDispatchPlan(%+v).Provider = %q, want %q", tc.in, got.Provider, tc.want)
			}
			if got.Endpoint != tc.in.Endpoint {
				t.Fatalf("BuildDispatchPlan(%+v).Endpoint = %q, want %q", tc.in, got.Endpoint, tc.in.Endpoint)
			}
		})
	}
}

func TestDispatchPlanUsesNewAPIBridge(t *testing.T) {
	if !(DispatchPlan{Provider: ProviderNewAPIBridge}.UsesNewAPIBridge()) {
		t.Fatal("newapi bridge provider must report UsesNewAPIBridge=true")
	}
	if (DispatchPlan{Provider: ProviderNative}.UsesNewAPIBridge()) {
		t.Fatal("native provider must report UsesNewAPIBridge=false")
	}
}

func TestOpenAICompatPlatforms(t *testing.T) {
	got := OpenAICompatPlatforms()
	if len(got) != 2 {
		t.Fatalf("OpenAICompatPlatforms() returned %d entries, want 2: %v", len(got), got)
	}

	want := map[string]bool{
		domain.PlatformOpenAI: false,
		domain.PlatformNewAPI: false,
	}
	for _, platform := range got {
		if _, ok := want[platform]; !ok {
			t.Fatalf("OpenAICompatPlatforms() returned unexpected platform %q", platform)
		}
		want[platform] = true
	}
	for platform, seen := range want {
		if !seen {
			t.Fatalf("OpenAICompatPlatforms() missing %q", platform)
		}
	}
}

func TestIsOpenAICompatPlatform(t *testing.T) {
	cases := []struct {
		platform string
		want     bool
	}{
		{domain.PlatformOpenAI, true},
		{domain.PlatformNewAPI, true},
		{domain.PlatformAnthropic, false},
		{domain.PlatformGemini, false},
		{domain.PlatformAntigravity, false},
		{"", false},
		{"unknown", false},
	}

	for _, tc := range cases {
		if got := IsOpenAICompatPlatform(tc.platform); got != tc.want {
			t.Fatalf("IsOpenAICompatPlatform(%q) = %v, want %v", tc.platform, got, tc.want)
		}
	}
}

func TestIsOpenAICompatPoolMember(t *testing.T) {
	cases := []struct {
		name            string
		accountPlatform string
		channelType     int
		groupPlatform   string
		want            bool
	}{
		{"openai matches openai", domain.PlatformOpenAI, 0, domain.PlatformOpenAI, true},
		{"newapi requires positive channel type", domain.PlatformNewAPI, 0, domain.PlatformNewAPI, false},
		{"newapi positive channel type allowed", domain.PlatformNewAPI, 45, domain.PlatformNewAPI, true},
		{"cross platform rejected", domain.PlatformNewAPI, 45, domain.PlatformOpenAI, false},
		{"empty account platform rejected", "", 45, domain.PlatformOpenAI, false},
		{"empty group platform rejected", domain.PlatformOpenAI, 0, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsOpenAICompatPoolMember(tc.accountPlatform, tc.channelType, tc.groupPlatform)
			if got != tc.want {
				t.Fatalf("IsOpenAICompatPoolMember(%q, %d, %q) = %v, want %v", tc.accountPlatform, tc.channelType, tc.groupPlatform, got, tc.want)
			}
		})
	}
}

func TestAllSchedulingPlatforms(t *testing.T) {
	got := AllSchedulingPlatforms()
	want := map[string]bool{
		domain.PlatformAnthropic:   false,
		domain.PlatformGemini:      false,
		domain.PlatformOpenAI:      false,
		domain.PlatformAntigravity: false,
		domain.PlatformNewAPI:      false,
	}
	if len(got) != len(want) {
		t.Fatalf("AllSchedulingPlatforms() returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for _, platform := range got {
		if _, ok := want[platform]; !ok {
			t.Fatalf("AllSchedulingPlatforms() returned unexpected platform %q", platform)
		}
		want[platform] = true
	}
	for platform, seen := range want {
		if !seen {
			t.Fatalf("AllSchedulingPlatforms() missing %q", platform)
		}
	}
}

func TestBridgeEndpointEnabled(t *testing.T) {
	cases := []struct {
		endpoint string
		want     bool
	}{
		{BridgeEndpointChatCompletions, true},
		{BridgeEndpointResponses, true},
		{BridgeEndpointEmbeddings, true},
		{BridgeEndpointImages, true},
		{BridgeEndpointVideoSubmit, true},
		{BridgeEndpointVideoFetch, true},
		{"", false},
		{"unknown", false},
	}

	for _, tc := range cases {
		if got := BridgeEndpointEnabled(tc.endpoint); got != tc.want {
			t.Fatalf("BridgeEndpointEnabled(%q) = %v, want %v", tc.endpoint, got, tc.want)
		}
	}
}

func TestCapabilityForEndpoint(t *testing.T) {
	capability, ok := CapabilityForEndpoint(BridgeEndpointVideoSubmit)
	if !ok {
		t.Fatal("CapabilityForEndpoint(video_submit) returned ok=false")
	}
	if capability.Endpoint != BridgeEndpointVideoSubmit {
		t.Fatalf("CapabilityForEndpoint(video_submit).Endpoint = %q, want %q", capability.Endpoint, BridgeEndpointVideoSubmit)
	}
	if capability.Provider != ProviderNewAPIBridge {
		t.Fatalf("CapabilityForEndpoint(video_submit).Provider = %q, want %q", capability.Provider, ProviderNewAPIBridge)
	}
	if !capability.RequiresChannelType {
		t.Fatal("video_submit capability must require channel_type")
	}
	if !capability.RequiresTaskAdaptor {
		t.Fatal("video_submit capability must require task adaptor")
	}
	if len(capability.SchedulingPlatforms) != 2 {
		t.Fatalf("video_submit SchedulingPlatforms len = %d, want 2", len(capability.SchedulingPlatforms))
	}

	if _, ok := CapabilityForEndpoint("unknown"); ok {
		t.Fatal("CapabilityForEndpoint(unknown) returned ok=true")
	}
}

func TestBridgeCapabilities(t *testing.T) {
	got := BridgeCapabilities()
	want := []string{
		BridgeEndpointChatCompletions,
		BridgeEndpointResponses,
		BridgeEndpointEmbeddings,
		BridgeEndpointImages,
		BridgeEndpointVideoSubmit,
		BridgeEndpointVideoFetch,
	}
	if len(got) != len(want) {
		t.Fatalf("BridgeCapabilities() len = %d, want %d", len(got), len(want))
	}
	for i, endpoint := range want {
		if got[i].Endpoint != endpoint {
			t.Fatalf("BridgeCapabilities()[%d].Endpoint = %q, want %q", i, got[i].Endpoint, endpoint)
		}
	}
}

func TestCapabilitySupportsSchedulingPlatform(t *testing.T) {
	capability, ok := CapabilityForEndpoint(BridgeEndpointChatCompletions)
	if !ok {
		t.Fatal("CapabilityForEndpoint(chat_completions) returned ok=false")
	}
	if !capability.SupportsSchedulingPlatform(domain.PlatformOpenAI) {
		t.Fatal("chat_completions must support openai scheduling platform")
	}
	if !capability.SupportsSchedulingPlatform(domain.PlatformNewAPI) {
		t.Fatal("chat_completions must support newapi scheduling platform")
	}
	if capability.SupportsSchedulingPlatform(domain.PlatformAnthropic) {
		t.Fatal("chat_completions must not support anthropic scheduling platform")
	}
}

func TestEndpointRequiresTaskAdaptor(t *testing.T) {
	if !EndpointRequiresTaskAdaptor(BridgeEndpointVideoSubmit) {
		t.Fatal("video_submit must require task adaptor")
	}
	if !EndpointRequiresTaskAdaptor(BridgeEndpointVideoFetch) {
		t.Fatal("video_fetch must require task adaptor")
	}
	if EndpointRequiresTaskAdaptor(BridgeEndpointChatCompletions) {
		t.Fatal("chat_completions must not require task adaptor")
	}
	if EndpointRequiresTaskAdaptor("unknown") {
		t.Fatal("unknown endpoint must not require task adaptor")
	}
}

func TestIsVideoSupportedChannelType(t *testing.T) {
	cases := []struct {
		name        string
		channelType int
		want        bool
	}{
		{"volcengine", 45, true},
		{"doubao_video", 54, true},
		{"unknown_zero", 0, false},
		{"unknown_negative", -1, false},
		{"unknown_high", 9999, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsVideoSupportedChannelType(tc.channelType); got != tc.want {
				t.Fatalf("IsVideoSupportedChannelType(%d) = %v, want %v", tc.channelType, got, tc.want)
			}
		})
	}
}
