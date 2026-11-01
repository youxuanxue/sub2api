package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
)

// ManagedBatchResultObjectStore is the private object-store surface required
// by the result finalizer. Tencent COS is supplied through the existing S3
// compatible implementation.
type ManagedBatchResultObjectStore interface {
	Upload(context.Context, string, io.Reader, string) (int64, error)
}

// ManagedBatchResultFinalizer materializes terminal item rows into the two
// files exposed by the OpenAI Batch contract. Object keys and file IDs are
// deterministic, so retries after a worker crash do not create duplicates.
type ManagedBatchResultFinalizer struct {
	Repo  *ManagedBatchRepository
	Store ManagedBatchResultObjectStore
}

func (f *ManagedBatchResultFinalizer) Finalize(ctx context.Context, batchID string) error {
	if f == nil || f.Repo == nil || f.Repo.db == nil || f.Store == nil || strings.TrimSpace(batchID) == "" {
		return errors.New("managed batch result finalizer is unavailable")
	}
	b, err := f.Repo.getBatchForResult(ctx, batchID)
	if err != nil {
		return err
	}
	items, err := f.Repo.listCompletedBatchItems(ctx, batchID)
	if err != nil {
		return err
	}
	output, failed, err := encodeBatchResults(items)
	if err != nil {
		return err
	}
	var outputID, errorID *string
	if len(output) > 0 {
		id := managedBatchResultFileID(batchID, "output")
		if err := f.uploadResult(ctx, b, id, "output", output); err != nil {
			return err
		}
		outputID = &id
	}
	if len(failed) > 0 {
		id := managedBatchResultFileID(batchID, "error")
		if err := f.uploadResult(ctx, b, id, "error", failed); err != nil {
			return err
		}
		errorID = &id
	}
	return f.Repo.setBatchResultFiles(ctx, batchID, outputID, errorID)
}

func (f *ManagedBatchResultFinalizer) uploadResult(ctx context.Context, b ManagedBatch, id, kind string, data []byte) error {
	key := "managed-batches/" + b.ID + "/" + kind + ".jsonl"
	if _, err := f.Store.Upload(ctx, key, bytes.NewReader(data), "application/jsonl"); err != nil {
		return fmt.Errorf("upload managed batch %s result: %w", kind, err)
	}
	hash := sha256.Sum256(data)
	expires := time.Now().UTC().Add(24 * time.Hour)
	_, err := f.Repo.db.ExecContext(ctx, `INSERT INTO batch_files (id,user_id,api_key_id,purpose,filename,storage_ref,byte_size,sha256,status,expires_at) VALUES ($1,$2,NULLIF($3,0),$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (id) DO NOTHING`, id, b.UserID, b.APIKeyID, batchcontract.FilePurposeBatch, "batch_"+b.ID+"_"+kind+".jsonl", key, len(data), fmt.Sprintf("%x", hash[:]), batchcontract.FileStatusProcessed, expires)
	if err != nil {
		return fmt.Errorf("persist managed batch %s result file: %w", kind, err)
	}
	return nil
}

func managedBatchResultFileID(batchID, kind string) string {
	digest := sha256.Sum256([]byte(batchID + ":" + kind))
	return "file_" + fmt.Sprintf("%x", digest[:16]) + "_" + kind
}

func (r *ManagedBatchRepository) getBatchForResult(ctx context.Context, id string) (ManagedBatch, error) {
	var b ManagedBatch
	var apiKeyID sql.NullInt64
	var metadata, counts []byte
	var outputID, errorID, code, message sql.NullString
	var retry sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT id,user_id,api_key_id,input_file_id,endpoint,completion_window,status,request_counts,output_file_id,error_file_id,metadata,last_error_code,last_error_message,deadline_at,retry_at,version,created_at FROM batches WHERE id=$1`, id).Scan(&b.ID, &b.UserID, &apiKeyID, &b.InputFileID, &b.Endpoint, &b.CompletionWindow, &b.Status, &counts, &outputID, &errorID, &metadata, &code, &message, &b.DeadlineAt, &retry, &b.Version, &b.CreatedAt)
	if err != nil {
		return ManagedBatch{}, err
	}
	if apiKeyID.Valid {
		b.APIKeyID = apiKeyID.Int64
	}
	if err := json.Unmarshal(counts, &b.RequestCounts); err != nil {
		return ManagedBatch{}, err
	}
	if err := json.Unmarshal(metadata, &b.Metadata); err != nil {
		return ManagedBatch{}, err
	}
	if outputID.Valid {
		b.OutputFileID = &outputID.String
	}
	if errorID.Valid {
		b.ErrorFileID = &errorID.String
	}
	if code.Valid {
		b.LastErrorCode = &code.String
	}
	if message.Valid {
		b.LastErrorMessage = &message.String
	}
	if retry.Valid {
		b.RetryAt = &retry.Time
	}
	return b, nil
}

func (r *ManagedBatchRepository) listCompletedBatchItems(ctx context.Context, batchID string) ([]ManagedBatchItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,batch_id,custom_id,status,response,error,input_tokens,output_tokens,total_tokens,unknown_result FROM batch_items WHERE batch_id=$1 AND status IN ('succeeded','failed','cancelled','unknown') ORDER BY id`, batchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []ManagedBatchItem
	for rows.Next() {
		var item ManagedBatchItem
		var response, itemErr []byte
		if err := rows.Scan(&item.ID, &item.BatchID, &item.CustomID, &item.Status, &response, &itemErr, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.UnknownResult); err != nil {
			return nil, err
		}
		if len(response) > 0 && string(response) != "null" {
			if err := json.Unmarshal(response, &item.Response); err != nil {
				return nil, err
			}
		}
		if len(itemErr) > 0 && string(itemErr) != "null" {
			if err := json.Unmarshal(itemErr, &item.Error); err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *ManagedBatchRepository) setBatchResultFiles(ctx context.Context, batchID string, outputID, errorID *string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE batches SET output_file_id=COALESCE(output_file_id,NULLIF($2,'')),error_file_id=COALESCE(error_file_id,NULLIF($3,'')),updated_at=NOW() WHERE id=$1`, batchID, managedBatchValueOrEmpty(outputID), managedBatchValueOrEmpty(errorID))
	return err
}

func encodeBatchResults(items []ManagedBatchItem) ([]byte, []byte, error) {
	var output, failed bytes.Buffer
	for _, item := range items {
		id := strconv.FormatInt(item.ID, 10)
		if item.Status == batchcontract.ItemStatusSucceeded {
			if item.Response == nil {
				return nil, nil, fmt.Errorf("managed batch item %s has no response", item.CustomID)
			}
			line, err := responseResultLine(id, item)
			if err != nil {
				return nil, nil, err
			}
			if err := writeJSONLine(&output, line); err != nil {
				return nil, nil, err
			}
			continue
		}
		line := batchcontract.ResultLine{ID: id, CustomID: item.CustomID, Error: errorResult(item)}
		if err := writeJSONLine(&failed, line); err != nil {
			return nil, nil, err
		}
	}
	return output.Bytes(), failed.Bytes(), nil
}

func responseResultLine(id string, item ManagedBatchItem) (batchcontract.ResultLine, error) {
	statusCode := intValue(item.Response["status_code"])
	requestID, _ := item.Response["request_id"].(string)
	body, ok := item.Response["body"]
	if !ok {
		return batchcontract.ResultLine{}, fmt.Errorf("managed batch item %s response body is missing", item.CustomID)
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return batchcontract.ResultLine{}, err
	}
	return batchcontract.ResultLine{ID: id, CustomID: item.CustomID, Response: &batchcontract.ItemResponse{StatusCode: statusCode, RequestID: requestID, Body: rawBody}}, nil
}

func errorResult(item ManagedBatchItem) *batchcontract.BatchError {
	code, _ := item.Error["code"].(string)
	message, _ := item.Error["message"].(string)
	if code == "" {
		code = item.Status
	}
	if message == "" {
		message = "managed batch item did not complete successfully"
	}
	return &batchcontract.BatchError{Code: code, Message: message}
}

func writeJSONLine(w *bytes.Buffer, value any) error {
	return json.NewEncoder(w).Encode(value)
}

func intValue(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		parsed, _ := strconv.Atoi(string(n))
		return parsed
	default:
		return 0
	}
}
