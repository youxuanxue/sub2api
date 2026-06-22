//go:build unit

package service

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNotifyClaudeAPIIncidentStarted_SendsP0Card(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	fixed := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, fixed)

	n.NotifyClaudeAPIIncidentStarted("partial_outage")
	<-doer.done

	require.Equal(t, 1, doer.callCount())
	body := doer.lastBody()
	require.Contains(t, body, "partial_outage")
	require.Contains(t, body, claudeStatusPageURL)
	require.Contains(t, body, "SetError")
}

func TestNotifyClaudeAPIIncidentStarted_DedupesWithinWindow(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 2)}
	fixed := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, fixed)

	n.NotifyClaudeAPIIncidentStarted("partial_outage")
	<-doer.done
	n.NotifyClaudeAPIIncidentStarted("major_outage")

	require.Equal(t, 1, doer.callCount())
}

func TestNotifyClaudeAPIIncidentResolved_OnlyAfterStarted(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 2)}
	fixed := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, fixed)

	n.NotifyClaudeAPIIncidentResolved("operational")
	require.Equal(t, 0, doer.callCount())

	n.NotifyClaudeAPIIncidentStarted("partial_outage")
	<-doer.done
	n.NotifyClaudeAPIIncidentResolved("operational")
	<-doer.done

	require.Equal(t, 2, doer.callCount())
	require.True(t, strings.Contains(doer.lastBody(), "operational"))
}

func TestSetClaudeAPIStatusNotifier_WiresPollerTransition(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	fixed := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, fixed)
	t.Cleanup(func() { SetClaudeAPIStatusNotifier(nil) })
	SetClaudeAPIStatusNotifier(n)

	claudeStatusAtom.Store(&ClaudeStatusSnapshot{IsIncident: false, Status: "operational", FetchedAt: fixed})
	getClaudeAPIStatusNotifier().NotifyClaudeAPIIncidentStarted("major_outage")
	<-doer.done
	require.Equal(t, 1, doer.callCount())
}
