package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

const workerTerminalReceiveCount = 3

type ExecuteFunc func(context.Context, JobSpec, archive.ReadOnlyObjectStore, Store, ExecuteDeps) (JobReceipt, error)

type Worker struct {
	Consumer    JobConsumer
	RawStore    archive.ReadOnlyObjectStore
	OutputStore Store
	RestoreRoot string
	Execute     ExecuteFunc
}

type JobFailure struct {
	SchemaVersion string  `json:"schema_version"`
	Kind          JobKind `json:"kind"`
	JobID         string  `json:"job_id"`
	Error         string  `json:"error"`
}

func (w Worker) RunOne(ctx context.Context) (bool, error) {
	if w.Consumer == nil || w.OutputStore == nil {
		return false, errors.New("qa bundle worker dependencies are incomplete")
	}
	message, ok, err := w.Consumer.Receive(ctx)
	if err != nil || !ok {
		return false, err
	}
	body, err := w.OutputStore.Read(ctx, message.SpecKey)
	if err != nil {
		return true, fmt.Errorf("read qa bundle job spec: %w", err)
	}
	spec, err := ParseJobSpec(body)
	if err != nil || spec.SpecKey != message.SpecKey {
		return true, errors.New("qa bundle queue message does not match immutable job spec")
	}
	completed, err := w.OutputStore.Head(ctx, spec.ReceiptKey)
	if err != nil {
		return true, err
	}
	if completed {
		return true, w.Consumer.Ack(ctx, message)
	}
	execute := w.Execute
	if execute == nil {
		execute = ExecuteJob
	}
	_, executeErr := execute(ctx, spec, w.RawStore, w.OutputStore, ExecuteDeps{RestoreRoot: w.RestoreRoot})
	if executeErr != nil {
		if message.ReceiveCount >= workerTerminalReceiveCount {
			failure := JobFailure{
				SchemaVersion: JobSchemaVersion, Kind: spec.Kind, JobID: spec.JobID,
				Error: terminalFailureCode(spec.Kind),
			}
			failureBody, marshalErr := json.Marshal(failure)
			if marshalErr != nil {
				return true, marshalErr
			}
			if publishErr := createOrVerify(ctx, w.OutputStore, spec.FailureKey, failureBody, ObjectMetadata{ContentType: "application/json"}); publishErr != nil {
				return true, fmt.Errorf("publish qa bundle failure receipt: %w", publishErr)
			}
		}
		return true, executeErr
	}
	if err := w.Consumer.Ack(ctx, message); err != nil {
		return true, fmt.Errorf("acknowledge qa bundle job: %w", err)
	}
	return true, nil
}

func terminalFailureCode(kind JobKind) string {
	if kind == JobKindBundleZip {
		return "export_failed"
	}
	return "bundle_failed"
}
