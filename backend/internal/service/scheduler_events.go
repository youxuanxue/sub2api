package service

import "github.com/Wei-Shaw/sub2api/internal/domain"

// Scheduler outbox event type constants — canonical definitions are in domain/.
const (
	SchedulerOutboxEventAccountChanged       = domain.SchedulerOutboxEventAccountChanged
	SchedulerOutboxEventAccountGroupsChanged = domain.SchedulerOutboxEventAccountGroupsChanged
	SchedulerOutboxEventAccountBulkChanged   = domain.SchedulerOutboxEventAccountBulkChanged
	SchedulerOutboxEventAccountLastUsed      = domain.SchedulerOutboxEventAccountLastUsed
	SchedulerOutboxEventGroupChanged         = domain.SchedulerOutboxEventGroupChanged
	SchedulerOutboxEventFullRebuild          = domain.SchedulerOutboxEventFullRebuild
)
