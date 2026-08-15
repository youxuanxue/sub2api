package handler

import "github.com/Wei-Shaw/sub2api/internal/observability/qa"

type QAHandler struct {
	service *qa.Service
}

func NewQAHandler(service *qa.Service) *QAHandler {
	return &QAHandler{service: service}
}
