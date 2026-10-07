package cursor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/google/uuid"
)

var ErrContinuationUnavailable = errors.New("tool continuation is unavailable or not authorized")
var errRunCapacity = errors.New("tool continuation capacity is exhausted")

const pendingToolPrefix = "toolu_gw_"

func newPendingToolID() string {
	return pendingToolPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

type RunOwner struct{ UserID, KeyID, AccountID int64 }
type runOwnerKey struct{}

// WithRunOwner accepts authenticated identity from the service boundary only.
// A public session ID or tool ID alone never authorizes a continuation.
func WithRunOwner(ctx context.Context, owner RunOwner) context.Context {
	return context.WithValue(ctx, runOwnerKey{}, owner)
}
func runOwnerFromContext(ctx context.Context) (RunOwner, bool) {
	owner, ok := ctx.Value(runOwnerKey{}).(RunOwner)
	return owner, ok && owner.UserID > 0 && owner.KeyID > 0 && owner.AccountID > 0
}

type nativeSegment struct {
	event  *AgentEvent
	result AgentResult
	err    error
}
type nativeRun struct {
	waiting     atomic.Bool
	owner       RunOwner
	fingerprint [32]byte
	history     [32]byte
	ctx         context.Context
	cancel      context.CancelFunc
	output      chan nativeSegment
	results     chan AgentMessage
	done        chan struct{}
}
type nativeRunManager struct {
	mu                            sync.Mutex
	runs                          map[*nativeRun]bool
	pending                       map[string]*nativeRun
	waitTTL, lifetime             time.Duration
	maxRuns, maxAccount, maxOwner int
}

func newNativeRunManager() *nativeRunManager {
	return &nativeRunManager{runs: map[*nativeRun]bool{}, pending: map[string]*nativeRun{}, waitTTL: 3 * time.Minute, lifetime: 15 * time.Minute, maxRuns: 32, maxAccount: 8, maxOwner: 4}
}

var nativeRuns = newNativeRunManager()

func appendHistoryDigest(digest [32]byte, messages []AgentMessage) [32]byte {
	for _, message := range messages {
		raw, _ := json.Marshal(message)
		digest = sha256.Sum256(append(digest[:], raw...))
	}
	return digest
}

func nativeFingerprint(input AgentRequest) [32]byte {
	data, _ := json.Marshal(struct {
		System, Model, Wire string
		Parameters          []Parameter
		Tools               []AgentTool
	}{input.System, input.Model, input.WireModel, input.Parameters, input.Tools})
	return sha256.Sum256(data)
}

// PendingToolIDs examines the turn since the latest assistant response. User
// text after a result must not hide a continuation and start a second run.
// Completed tools behind an assistant response remain ordinary history.
func PendingToolIDs(body []byte) []string {
	var root struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	items := root.Messages
	if len(items) == 0 && len(root.Input) > 0 && root.Input[0] == '[' {
		_ = json.Unmarshal(root.Input, &items)
	}
	ids := []string{}
	add := func(id string) {
		if strings.HasPrefix(id, pendingToolPrefix) {
			ids = append(ids, id)
		}
	}
	for i := len(items) - 1; i >= 0; i-- {
		var item struct {
			Role, Type string
			ToolCallID string `json:"tool_call_id"`
			CallID     string `json:"call_id"`
			Content    json.RawMessage
		}
		if json.Unmarshal(items[i], &item) != nil {
			break
		}
		if item.Type == "function_call_output" {
			add(item.CallID)
			continue
		}
		if item.Role == "tool" {
			add(item.ToolCallID)
			continue
		}
		if item.Role == "system" || item.Role == "developer" {
			continue // Instructions do not complete the pending assistant turn.
		}
		if item.Role == "user" {
			var blocks []struct {
				Type string
				ID   string `json:"tool_use_id"`
			}
			if json.Unmarshal(item.Content, &blocks) == nil {
				for _, b := range blocks {
					if b.Type == "tool_result" {
						add(b.ID)
					}
				}
			}
			continue
		}
		break
	}
	return ids
}

// ContinuationAccount is a read-only affinity lookup before normal scheduling.
// The scheduler must still enforce current account/group/Plan authorization.
func ContinuationAccount(userID, keyID int64, body []byte) (int64, error) {
	ids := PendingToolIDs(body)
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) != 1 {
		return 0, ErrContinuationUnavailable
	}
	nativeRuns.mu.Lock()
	defer nativeRuns.mu.Unlock()
	run := nativeRuns.pending[ids[0]]
	if run == nil || run.ctx.Err() != nil || run.owner.UserID != userID || run.owner.KeyID != keyID {
		return 0, ErrContinuationUnavailable
	}
	return run.owner.AccountID, nil
}

func (m *nativeRunManager) remove(run *nativeRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.runs, run)
	for id, value := range m.pending {
		if value == run {
			delete(m.pending, id)
		}
	}
}
func (m *nativeRunManager) abort(run *nativeRun) { run.cancel(); m.remove(run) }

func nativeResultToResume(input AgentRequest) (AgentMessage, bool, error) {
	if len(input.Messages) == 0 {
		return AgentMessage{}, false, nil
	}
	last := input.Messages[len(input.Messages)-1]
	if last.Role != "tool" || !strings.HasPrefix(last.ToolCallID, pendingToolPrefix) {
		// Only a sole final tool result can resume the retained duplex. Reject
		// trailing user text rather than replaying a possibly completed action.
		for i := len(input.Messages) - 1; i >= 0; i-- {
			message := input.Messages[i]
			if message.Role != "user" && message.Role != "tool" {
				break
			}
			if message.Role == "tool" && strings.HasPrefix(message.ToolCallID, pendingToolPrefix) {
				return AgentMessage{}, true, ErrContinuationUnavailable
			}
		}
		return AgentMessage{}, false, nil
	}
	count := 0
	for i := len(input.Messages) - 1; i >= 0 && input.Messages[i].Role == "tool"; i-- {
		count++
	}
	if count != 1 || len(last.Text) > maxAgentFrame/2 {
		return AgentMessage{}, true, ErrContinuationUnavailable
	}
	return last, true, nil
}
func hasNativeClientTools(input AgentRequest) bool {
	for _, tool := range input.Tools {
		switch tool.Name {
		case "Read", "Write", "Bash", "Grep":
			return true
		}
	}
	return false
}

func (m *nativeRunManager) segment(ctx context.Context, token string, input AgentRequest, do func(*http.Request) (*http.Response, error), emit func(AgentEvent) error) (AgentResult, *nativeRun, error) {
	owner, owned := runOwnerFromContext(ctx)
	clientResult, resuming, err := nativeResultToResume(input)
	if err != nil {
		return AgentResult{}, nil, err
	}
	if !resuming && (!owned || !hasNativeClientTools(input)) {
		result, err := RunAgent(ctx, token, input, do, emit)
		return result, nil, err
	}
	if !owned {
		return AgentResult{}, nil, ErrContinuationUnavailable
	}
	fingerprint := nativeFingerprint(input)
	m.mu.Lock()
	var run *nativeRun
	if resuming {
		run = m.pending[clientResult.ToolCallID]
		if run == nil || run.ctx.Err() != nil || run.owner != owner || run.fingerprint != fingerprint || (len(input.Messages) > 1 && appendHistoryDigest([32]byte{}, input.Messages[:len(input.Messages)-1]) != run.history) {
			m.mu.Unlock()
			return AgentResult{}, nil, ErrContinuationUnavailable
		}
		// Consuming under the same lock prevents concurrent duplicate submission.
		delete(m.pending, clientResult.ToolCallID)
		run.history = appendHistoryDigest(run.history, []AgentMessage{clientResult})
	} else {
		byOwner, byAccount := 0, 0
		for existing := range m.runs {
			if existing.owner.UserID == owner.UserID && existing.owner.KeyID == owner.KeyID {
				byOwner++
			}
			if existing.owner.AccountID == owner.AccountID {
				byAccount++
			}
		}
		if len(m.runs) >= m.maxRuns || byOwner >= m.maxOwner || byAccount >= m.maxAccount {
			m.mu.Unlock()
			return AgentResult{}, nil, errRunCapacity
		}
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.lifetime)
		run = &nativeRun{owner: owner, fingerprint: fingerprint, history: appendHistoryDigest([32]byte{}, input.Messages), ctx: runCtx, cancel: cancel, output: make(chan nativeSegment), results: make(chan AgentMessage, 1), done: make(chan struct{})}
		m.runs[run] = true
	}
	m.mu.Unlock()
	if resuming {
		select {
		case run.results <- clientResult:
		case <-run.ctx.Done():
			m.abort(run)
			return AgentResult{}, nil, ErrContinuationUnavailable
		}
	} else {
		input.handoff = func(runCtx context.Context, exec *pb.ExecServerMessage, result AgentResult) (AgentMessage, error) {
			if len(result.ToolCalls) != 1 {
				return AgentMessage{}, errAgentToolProtocol
			}
			id := result.ToolCalls[0].ID
			m.mu.Lock()
			m.pending[id] = run
			run.history = appendHistoryDigest(run.history, []AgentMessage{{Role: "assistant", Text: result.Text, ToolCalls: result.ToolCalls}})
			m.mu.Unlock()
			defer func() { m.mu.Lock(); delete(m.pending, id); m.mu.Unlock() }()
			select {
			case run.output <- nativeSegment{result: result}:
			case <-runCtx.Done():
				return AgentMessage{}, runCtx.Err()
			}
			run.waiting.Store(true)
			timer := time.NewTimer(m.waitTTL)
			defer timer.Stop()
			select {
			case response := <-run.results:
				run.waiting.Store(false)
				return response, nil
			case <-timer.C:
				return AgentMessage{}, context.DeadlineExceeded
			case <-runCtx.Done():
				return AgentMessage{}, runCtx.Err()
			}
		}
		go func() {
			defer close(run.done)
			defer m.abort(run)
			result, err := RunAgent(run.ctx, token, input, do, func(event AgentEvent) error {
				select {
				case run.output <- nativeSegment{event: &event}:
					return nil
				case <-run.ctx.Done():
					return run.ctx.Err()
				}
			})
			if run.waiting.Load() {
				return
			} // no HTTP consumer remains after an expired handoff
			select {
			case run.output <- nativeSegment{result: result, err: err}:
			case <-run.ctx.Done():
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			m.abort(run)
			return AgentResult{}, nil, ctx.Err()
		case <-run.ctx.Done():
			m.abort(run)
			return AgentResult{}, nil, ErrContinuationUnavailable
		case item := <-run.output:
			if item.event != nil {
				if emit != nil {
					if err := emit(*item.event); err != nil {
						m.abort(run)
						return AgentResult{}, nil, err
					}
				}
				continue
			}
			return item.result, run, item.err
		}
	}
}
