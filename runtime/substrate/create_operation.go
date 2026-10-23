package substrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/agent-primitives/sessions/api"
	"github.com/google/uuid"
)

// OperationControl is generic compute-owner idempotence, not enforcement.
// Original response identity is server-assigned. retireOnly atomically cancels
// an uncommitted create or returns its original UID; it never creates compute.
type OperationControl interface {
	CreateActorWithRequirementOperation(context.Context, ActorRef, ObjectRef, []byte, string, bool, bool) (ActorInfo, error)
}

type createOperation struct {
	Version     int       `json:"version"`
	ID          string    `json:"operation_id"`
	Actor       ActorRef  `json:"actor"`
	Template    ObjectRef `json:"template"`
	Requirement []byte    `json:"requirement"`
}

func (b *Backend) PrepareExecutionOperation(sessionUID string, raw []byte) ([]byte, error) {
	if _, ok := b.ctl.(OperationControl); !ok {
		return nil, fmt.Errorf("control lacks durable create-operation support")
	}
	if sessionUID == "" || len(raw) == 0 {
		return nil, fmt.Errorf("operation requires exact target and execution input")
	}
	return json.Marshal(createOperation{Version: 1, ID: uuid.NewString(), Actor: b.ref(sessionUID), Template: b.template, Requirement: append([]byte(nil), raw...)})
}

func (b *Backend) parseOperation(raw []byte) (createOperation, error) {
	var op createOperation
	if len(raw) == 0 || len(raw) > 2<<20 {
		return op, fmt.Errorf("invalid compute operation size")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&op); err != nil {
		return op, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return op, fmt.Errorf("trailing compute operation data")
	}
	id, e := uuid.Parse(op.ID)
	if e != nil || id.String() != op.ID || op.Version != 1 || op.Actor.Name == "" || op.Actor.Atespace != b.atespace || len(op.Requirement) == 0 {
		return op, fmt.Errorf("invalid original compute operation")
	}
	// Old template belongs to the original operation, not today's mutable config.
	if op.Template.Atespace == "" || op.Template.Name == "" {
		return op, fmt.Errorf("operation lacks original template")
	}
	return op, nil
}

func (b *Backend) RetireExecutionOperation(ctx context.Context, raw []byte) error {
	op, err := b.parseOperation(raw)
	if err != nil {
		return err
	}
	ctl, ok := b.ctl.(OperationControl)
	if !ok {
		return fmt.Errorf("control lacks original operation retirement")
	}
	info, err := ctl.CreateActorWithRequirementOperation(ctx, op.Actor, op.Template, op.Requirement, op.ID, false, true)
	if err != nil {
		return fmt.Errorf("retire original create operation: %w", err)
	}
	// Empty UID is the owner's atomic cancellation tombstone, NOT an absent
	// name lookup. No delayed create on this operation can commit after success.
	if info.UID == "" {
		return nil
	}
	if info.Status != StatusSuspended || info.ExecutionRequirement != string(op.Requirement) {
		return fmt.Errorf("original create response is not exact suspended authority")
	}
	terminator, ok := b.ctl.(ExactTerminator)
	if !ok {
		return fmt.Errorf("control lacks exact original operation termination")
	}
	return terminator.TerminateActor(ctx, op.Actor, info.UID)
}

func (b *Backend) createWithOperation(ctx context.Context, s *api.SessionSpec) (owned api.Incarnation, err error) {
	op, err := b.parseOperation(s.ComputeOperation)
	if err != nil {
		return owned, err
	}
	if op.Actor != b.ref(s.SessionUID) || !bytes.Equal(op.Requirement, s.ExecutionRequirement) {
		return owned, fmt.Errorf("original operation target or requirement changed")
	}
	ctl, ok := b.ctl.(OperationControl)
	if !ok || s.RetainCompute == nil {
		return owned, fmt.Errorf("durable operation requires owner control and retention callback")
	}
	exact, ok := b.ctl.(ExactRequirementControl)
	if !ok {
		return owned, fmt.Errorf("control lacks exact UID resume")
	}
	verifier, ok := b.ctl.(ExactExecutionVerifier)
	if !ok {
		return owned, fmt.Errorf("control lacks current exact execution verification")
	}
	var original ActorInfo
	// An already-retained UID must never cause creation if its operation is
	// unexpectedly missing. Lookup-only cannot duplicate even after deletion.
	original, err = ctl.CreateActorWithRequirementOperation(ctx, op.Actor, op.Template, op.Requirement, op.ID, s.ExpectedComputeInstance != "", false)
	if err != nil {
		return owned, fmt.Errorf("resolve original create operation: %w", err)
	}
	owned = api.Incarnation{ID: s.SessionUID, Runtime: "substrate", ComputeInstance: original.UID, ExecutionRequirement: append([]byte(nil), op.Requirement...)}
	if original.UID == "" || original.Status != StatusSuspended || original.ExecutionRequirement != string(op.Requirement) {
		return owned, fmt.Errorf("operation response lacks exact original suspended identity")
	}
	if s.ExpectedComputeInstance != "" && s.ExpectedComputeInstance != original.UID {
		return owned, fmt.Errorf("operation differs from retained original UID")
	}
	if s.ExpectedComputeInstance == "" {
		callback := owned
		callback.ExecutionRequirement = append([]byte(nil), op.Requirement...)
		if err = s.RetainCompute(callback); err != nil {
			return owned, fmt.Errorf("retain original operation identity before resume: %w", err)
		}
	}
	current, err := b.ctl.GetActor(ctx, op.Actor)
	if err != nil {
		return owned, fmt.Errorf("original operation target unavailable; no replacement: %w", err)
	}
	if current.UID != original.UID || current.ExecutionRequirement != string(op.Requirement) {
		return owned, fmt.Errorf("operation target replaced or requirement changed")
	}
	if err = ctx.Err(); err != nil {
		return owned, err
	}
	switch current.Status {
	case StatusSuspended:
		current, err = exact.ResumeActorExact(ctx, op.Actor, original.UID)
		if err != nil {
			return owned, fmt.Errorf("resume original operation: %w", err)
		}
	case StatusRunning:
		// Lost Resume reply is recoverable only because both operation and original
		// UID are now retained. Running status itself still grants no dispatch.
	default:
		return owned, fmt.Errorf("original operation target not ready")
	}
	if current.UID != original.UID || current.ExecutionRequirement != string(op.Requirement) || current.Status != StatusRunning {
		return owned, fmt.Errorf("resumed original operation identity changed")
	}
	if err = verifier.VerifyActorExecution(ctx, op.Actor, original.UID, append([]byte(nil), op.Requirement...)); err != nil {
		return owned, fmt.Errorf("current operation execution verification: %w", err)
	}
	in, e := b.incarnation(s.SessionUID, current)
	if e != nil {
		return owned, e
	}
	return in, nil
}
