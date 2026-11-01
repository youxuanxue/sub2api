package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/handler"
)

// registerTKManagedBatchRoutes is intentionally a no-op while the backend
// adapter is not wired.  Keeping all paths behind the handler's Enabled gate
// lets deployments ship the contract without exposing an incomplete executor.
func registerTKManagedBatchRoutes(routes terminalRouteRegistrar, h *handler.Handlers) {
	if routes.routes == nil || h == nil || h.ManagedBatch == nil {
		return
	}
	routes.Register(http.MethodPost, "/files", Excluded("batch_file_upload"), h.ManagedBatch.FilesCreate)
	routes.Register(http.MethodGet, "/files", Excluded("batch_file_list"), h.ManagedBatch.FilesList)
	routes.Register(http.MethodGet, "/files/:file_id", Excluded("batch_file_read"), h.ManagedBatch.FilesGet)
	routes.Register(http.MethodGet, "/files/:file_id/content", Excluded("batch_file_content"), h.ManagedBatch.FilesContent)
	routes.Register(http.MethodDelete, "/files/:file_id", Excluded("batch_file_delete"), h.ManagedBatch.FilesDelete)
	routes.Register(http.MethodPost, "/batches", AsyncSubmission, h.ManagedBatch.BatchesCreate)
	routes.Register(http.MethodGet, "/batches", Excluded("batch_status"), h.ManagedBatch.BatchesList)
	routes.Register(http.MethodGet, "/batches/:batch_id", Excluded("batch_status"), h.ManagedBatch.BatchesGet)
	routes.Register(http.MethodPost, "/batches/:batch_id/cancel", Excluded("batch_control"), h.ManagedBatch.BatchesCancel)
	routes.Register(http.MethodPost, "/batches/:batch_id/retry", Excluded("batch_control"), h.ManagedBatch.BatchesRetry)
}
