package matchlobby

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// P2PLifecycle records a scoped native result/return event for a P2P HOST.
// The actor is returned as the snapshot viewer so local capabilities remain
// useful during the ENDING phase.
func (s *Service) P2PLifecycle(
	ctx context.Context,
	actor Actor,
	authoritySession, attemptID string,
	input LifecycleInput,
) (Snapshot, error) {
	if err := requireActive(actor); err != nil {
		return Snapshot{}, err
	}
	return s.lifecycle(ctx, actor.PlayerID, authoritySession, attemptID, HostingP2P, input, actor.PlayerID)
}

// DedicatedLifecycle records a scoped native result/return event for a
// Dedicated authority. The authority session remains the sole credential.
func (s *Service) DedicatedLifecycle(
	ctx context.Context,
	serverID, authoritySession, attemptID string,
	input LifecycleInput,
) (Snapshot, error) {
	return s.lifecycle(ctx, serverID, authoritySession, attemptID, HostingDedicated, input, "")
}

func (s *Service) lifecycle(
	ctx context.Context,
	authorityID, authoritySession, attemptID string,
	hosting HostingKind,
	input LifecycleInput,
	viewerPlayerID string,
) (Snapshot, error) {
	authorityID = strings.TrimSpace(authorityID)
	authoritySession = strings.TrimSpace(authoritySession)
	attemptID = strings.TrimSpace(attemptID)
	input.WorldInstanceID = strings.TrimSpace(input.WorldInstanceID)
	if authorityID == "" || authoritySession == "" || attemptID == "" ||
		input.EventSeq < 1 || input.RosterRevision < 1 || input.RouteGeneration < 1 ||
		input.MatchGeneration < 1 || !worldInstancePattern.MatchString(input.WorldInstanceID) {
		return Snapshot{}, invalid("Invalid match lifecycle scope.", nil)
	}
	if input.Phase != LifecycleResultConfirmed && input.Phase != LifecycleReturnReady {
		return Snapshot{}, invalid("Invalid match lifecycle phase.", nil)
	}
	expectedSeq := int64(1)
	if input.Phase == LifecycleReturnReady {
		expectedSeq = 2
	}
	if input.EventSeq != expectedSeq {
		return Snapshot{}, invalid("Match lifecycle event sequence is invalid.", map[string]any{"expected_event_seq": expectedSeq})
	}

	now := s.now().UTC()
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var lobbyID string
	var state AttemptState
	var storedHosting, storedWorld, lifecyclePhase string
	var storedRoster, storedRoute, storedMatchGeneration int
	var storedEventSeq int64
	err = tx.QueryRow(ctx, `
		SELECT lobby_id, hosting_kind, state, COALESCE(world_instance_id, ''),
		       roster_revision, route_generation, match_generation,
		       lifecycle_phase, lifecycle_event_seq
		FROM match_attempts
		WHERE id = $1 AND authority_id = $2 AND authority_session_id = $3
		  AND hosting_kind = $4
		FOR UPDATE
	`, attemptID, authorityID, authoritySession, hosting).Scan(
		&lobbyID, &storedHosting, &state, &storedWorld, &storedRoster,
		&storedRoute, &storedMatchGeneration, &lifecyclePhase, &storedEventSeq,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, forbidden("MATCH_AUTHORITY_SCOPE_REQUIRED", "The authority session does not own this match attempt.")
	}
	if err != nil {
		return Snapshot{}, internal(err)
	}
	if storedHosting != string(hosting) || storedWorld != input.WorldInstanceID ||
		int64(storedRoster) != input.RosterRevision || storedRoute != input.RouteGeneration ||
		storedMatchGeneration != input.MatchGeneration {
		return Snapshot{}, conflict("MATCH_LIFECYCLE_SCOPE_CONFLICT", "The lifecycle event does not match the frozen native scope.", nil)
	}

	// The exact same event is safe to replay, including after final completion.
	if lifecyclePhase == string(input.Phase) && storedEventSeq == input.EventSeq {
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		// RETURN_READY is persisted before Complete performs the terminal
		// transition. If that second transaction failed, a durable outbox retry
		// must finish the transition instead of treating the event as complete
		// while leaving the attempt in ENDING.
		if input.Phase == LifecycleReturnReady && state == AttemptEnding {
			completed, err := s.Complete(ctx, authorityID, authoritySession, attemptID, true, "")
			if err != nil {
				return Snapshot{}, err
			}
			if viewerPlayerID != "" {
				return s.Get(ctx, lobbyID, viewerPlayerID)
			}
			return completed, nil
		}
		return s.Get(ctx, lobbyID, viewerPlayerID)
	}
	// RESULT_CONFIRMED is a durable one-shot receipt.  A delayed seq=1
	// delivery may arrive after RETURN_READY/Complete(seq=2); accept it as an
	// idempotent replay instead of wedging the native outbox on a conflict.
	if input.Phase == LifecycleResultConfirmed && input.EventSeq == 1 &&
		storedEventSeq >= 1 && lifecyclePhase != "" {
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, internal(err)
		}
		return s.Get(ctx, lobbyID, viewerPlayerID)
	}
	if storedEventSeq >= input.EventSeq ||
		(input.Phase == LifecycleResultConfirmed && lifecyclePhase != "") {
		return Snapshot{}, conflict("MATCH_LIFECYCLE_EVENT_CONFLICT", "The match lifecycle already recorded a different event.", nil)
	}

	switch input.Phase {
	case LifecycleResultConfirmed:
		if state != AttemptRunning {
			return Snapshot{}, conflict("MATCH_ATTEMPT_NOT_RUNNING", "A result can be confirmed only while the attempt is running.", nil)
		}
		endingDeadline := now.Add(s.endingTimeout())
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempts
			SET state = 'ENDING', lifecycle_phase = 'RESULT_CONFIRMED',
			    lifecycle_event_seq = $2, result_confirmed_at = COALESCE(result_confirmed_at, $3),
			    ending_deadline = $4, authority_last_seen_at = $3, updated_at = $3
			WHERE id = $1
		`, attemptID, input.EventSeq, now, endingDeadline); err != nil {
			return Snapshot{}, internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_lobbies SET state = 'ENDING', updated_at = $2
			WHERE id = $1 AND current_attempt_id = $3 AND state = 'RUNNING'
		`, lobbyID, now, attemptID); err != nil {
			return Snapshot{}, internal(err)
		}
		// A result-confirmed attempt admits no new seats. Already connected
		// players continue to report disconnects during the result/return window.
		if _, err := tx.Exec(ctx, `
			UPDATE match_admission_grants
			SET revoked_at = $2
			WHERE attempt_id = $1 AND revoked_at IS NULL AND consumed_at IS NULL
		`, attemptID, now); err != nil {
			return Snapshot{}, internal(err)
		}
	case LifecycleReturnReady:
		if state == AttemptCompleted && lifecyclePhase == string(LifecycleResultConfirmed) {
			// The result watchdog may have completed the attempt while the
			// native durable outbox was offline.  Preserve the known result but
			// record the late return receipt so cleanup/native-clear retries can
			// advance without a permanent 409.
			if _, err := tx.Exec(ctx, `
				UPDATE match_attempts
				SET lifecycle_phase = 'RETURN_READY', lifecycle_event_seq = $2,
				    return_ready_at = COALESCE(return_ready_at, $3), updated_at = $3
				WHERE id = $1 AND state = 'COMPLETED' AND lifecycle_phase = 'RESULT_CONFIRMED'
			`, attemptID, input.EventSeq, now); err != nil {
				return Snapshot{}, internal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				return Snapshot{}, internal(err)
			}
			return s.Get(ctx, lobbyID, viewerPlayerID)
		}
		if state != AttemptEnding || lifecyclePhase != string(LifecycleResultConfirmed) {
			return Snapshot{}, conflict("MATCH_RESULT_NOT_CONFIRMED", "Return readiness requires a prior result confirmation.", nil)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_attempts
			SET lifecycle_phase = 'RETURN_READY', lifecycle_event_seq = $2,
			    return_ready_at = COALESCE(return_ready_at, $3),
			    authority_last_seen_at = $3, updated_at = $3
			WHERE id = $1 AND state = 'ENDING'
		`, attemptID, input.EventSeq, now); err != nil {
			return Snapshot{}, internal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, internal(err)
	}
	if input.Phase == LifecycleReturnReady {
		// Complete accepts ENDING only after RETURN_READY. A process-loss call
		// after RESULT_CONFIRMED is also converted to successful completion by
		// Complete so a confirmed result cannot be overwritten by abort.
		completed, err := s.Complete(ctx, authorityID, authoritySession, attemptID, true, "")
		if err != nil {
			return Snapshot{}, err
		}
		if viewerPlayerID != "" {
			return s.Get(ctx, lobbyID, viewerPlayerID)
		}
		return completed, nil
	}
	return s.Get(ctx, lobbyID, viewerPlayerID)
}

func (s *Service) endingTimeout() time.Duration {
	seconds := s.config.EndingSeconds
	if seconds <= 0 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}
