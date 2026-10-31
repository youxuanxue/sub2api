package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrFeedbackNotFound        = infraerrors.NotFound("FEEDBACK_NOT_FOUND", "feedback ticket not found")
	ErrFeedbackInvalidFields   = infraerrors.BadRequest("FEEDBACK_INVALID_FIELDS", "invalid feedback fields")
	ErrFeedbackInvalidCategory = infraerrors.BadRequest("FEEDBACK_INVALID_CATEGORY", "invalid feedback category")
	ErrFeedbackInvalidStatus   = infraerrors.BadRequest("FEEDBACK_INVALID_STATUS", "invalid feedback status")
)

type feedbackConfigProvider interface {
	GetEmailNotificationConfig(context.Context) (*OpsEmailNotificationConfig, error)
}

type FeedbackTicket struct {
	ID        int64     `json:"id"`
	TicketNo  string    `json:"ticket_no"`
	UserID    int64     `json:"user_id"`
	Category  string    `json:"category"`
	Subject   string    `json:"subject"`
	Content   string    `json:"content"`
	RequestID string    `json:"request_id,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type FeedbackService struct {
	db          *sql.DB
	cfgProvider feedbackConfigProvider
	httpClient  *http.Client
}

func NewFeedbackService(db *sql.DB, cfgProvider feedbackConfigProvider) *FeedbackService {
	return &FeedbackService{db: db, cfgProvider: cfgProvider, httpClient: &http.Client{Timeout: opsFeishuWebhookTimeout}}
}

func (s *FeedbackService) Create(ctx context.Context, userID int64, category, subject, content, requestID string) (*FeedbackTicket, error) {
	category = strings.TrimSpace(category)
	subject = strings.TrimSpace(subject)
	content = strings.TrimSpace(content)
	if userID <= 0 || subject == "" || content == "" || len([]rune(subject)) > 120 || len([]rune(content)) > 4000 || len(requestID) > 128 {
		return nil, ErrFeedbackInvalidFields
	}
	switch category {
	case "api", "billing", "technical", "other":
	default:
		return nil, ErrFeedbackInvalidCategory
	}
	var ticket FeedbackTicket
	err := s.db.QueryRowContext(ctx, `
		WITH next_ticket AS (SELECT nextval('feedback_tickets_id_seq') AS id)
		INSERT INTO feedback_tickets (id, ticket_no, user_id, category, subject, content, request_id)
		SELECT id, 'TK-' || to_char(NOW(), 'YYYYMMDD') || '-' || lpad(id::text, 6, '0'), $1, $2, $3, $4, NULLIF($5, '')
		FROM next_ticket
		RETURNING id, ticket_no, user_id, category, subject, content, COALESCE(request_id, ''), status, created_at, updated_at`, userID, category, subject, content, strings.TrimSpace(requestID)).Scan(
		&ticket.ID, &ticket.TicketNo, &ticket.UserID, &ticket.Category, &ticket.Subject, &ticket.Content, &ticket.RequestID, &ticket.Status, &ticket.CreatedAt, &ticket.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.notify(ctx, &ticket, "created")
	return &ticket, nil
}

func (s *FeedbackService) List(ctx context.Context, userID int64, admin bool) ([]FeedbackTicket, error) {
	query := `SELECT id, ticket_no, user_id, category, subject, content, COALESCE(request_id, ''), status, created_at, updated_at FROM feedback_tickets`
	args := []any{}
	if !admin {
		query += " WHERE user_id = $1"
		args = append(args, userID)
	}
	query += " ORDER BY created_at DESC LIMIT 100"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]FeedbackTicket, 0)
	for rows.Next() {
		var t FeedbackTicket
		if err := rows.Scan(&t.ID, &t.TicketNo, &t.UserID, &t.Category, &t.Subject, &t.Content, &t.RequestID, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *FeedbackService) Get(ctx context.Context, id, userID int64, admin bool) (*FeedbackTicket, error) {
	query := `SELECT id, ticket_no, user_id, category, subject, content, COALESCE(request_id, ''), status, created_at, updated_at FROM feedback_tickets WHERE id = $1`
	args := []any{id}
	if !admin {
		query += " AND user_id = $2"
		args = append(args, userID)
	}
	var t FeedbackTicket
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&t.ID, &t.TicketNo, &t.UserID, &t.Category, &t.Subject, &t.Content, &t.RequestID, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrFeedbackNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *FeedbackService) UpdateStatus(ctx context.Context, id int64, status string) (*FeedbackTicket, error) {
	switch status {
	case "open", "processing", "resolved", "closed":
	default:
		return nil, ErrFeedbackInvalidStatus
	}
	result, err := s.db.ExecContext(ctx, `UPDATE feedback_tickets SET status = $1, updated_at = NOW(), resolved_at = CASE WHEN $1 IN ('resolved','closed') THEN NOW() ELSE NULL END WHERE id = $2`, status, id)
	if err != nil {
		return nil, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if count == 0 {
		return nil, ErrFeedbackNotFound
	}
	t, err := s.Get(ctx, id, 0, true)
	if err == nil {
		s.notify(ctx, t, "status="+status)
	}
	return t, err
}

func (s *FeedbackService) notify(ctx context.Context, t *FeedbackTicket, change string) {
	if s == nil || s.cfgProvider == nil || t == nil {
		return
	}
	cfg, err := s.cfgProvider.GetEmailNotificationConfig(ctx)
	if err != nil || cfg == nil || !cfg.Feishu.Enabled || strings.TrimSpace(cfg.Feishu.WebhookURL) == "" {
		return
	}
	body := fmt.Sprintf("工单 %s 已%s\n类别：%s\n标题：%s\n用户：%d\n内容：%s", t.TicketNo, change, t.Category, redactFeedback(truncateFeedback(t.Subject, 120)), t.UserID, redactFeedback(truncateFeedback(t.Content, 500)))
	payload := feishuCardPayload(cfg.Feishu, time.Now, "blue", "TokenKey 工单更新", body)
	if err := sendFeishuPayload(ctx, s.httpClient, cfg.Feishu, payload); err != nil {
		// Notification is best effort; ticket state remains authoritative.
		return
	}
}

func truncateFeedback(v string, n int) string {
	r := []rune(v)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return v
}

var (
	feedbackSecretAssignment = regexp.MustCompile(`(?i)(api[_ -]?key|access[_ -]?token|authorization|cookie|password|passwd|secret)\s*[:=]\s*[^\s,;]+`)
	feedbackBearerToken      = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	feedbackAPIKey           = regexp.MustCompile(`\b(?:sk|tk|key)-[A-Za-z0-9_-]{12,}\b`)
)

func redactFeedback(v string) string {
	v = feedbackBearerToken.ReplaceAllString(v, "Bearer [REDACTED]")
	v = feedbackSecretAssignment.ReplaceAllString(v, "$1=[REDACTED]")
	return feedbackAPIKey.ReplaceAllString(v, "[REDACTED]")
}
