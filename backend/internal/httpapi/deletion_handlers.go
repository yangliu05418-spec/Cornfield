package httpapi

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
)

func (s *Server) deleteAsset(w http.ResponseWriter, r *http.Request) {
	assetID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_DELETE_FAILED", "删除资产失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var ownerID uuid.UUID
	var pending bool
	var purged bool
	err = tx.QueryRow(r.Context(), `SELECT owner_user_id,purge_pending,purged_at IS NOT NULL FROM assets
		WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, assetID, sess.UserID).Scan(&ownerID, &pending, &purged)
	if isNotFound(err) || purged {
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "资产不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_DELETE_FAILED", "删除资产失败", true, r)
		return
	}
	var requestID uuid.UUID
	if pending {
		err = tx.QueryRow(r.Context(), `SELECT id FROM deletion_requests WHERE asset_id=$1 AND status IN ('pending','running')`, assetID).Scan(&requestID)
		if isNotFound(err) {
			err = tx.QueryRow(r.Context(), `INSERT INTO deletion_requests(kind,owner_user_id,asset_id,requested_by)
				VALUES('asset',$1,$2,$3) RETURNING id`, ownerID, assetID, sess.UserID).Scan(&requestID)
		}
	} else {
		_, err = tx.Exec(r.Context(), `UPDATE assets SET purge_pending=true WHERE id=$1`, assetID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE generation_outputs SET deleted_at=COALESCE(deleted_at,now()) WHERE asset_id=$1`, assetID)
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO deletion_requests(kind,owner_user_id,asset_id,requested_by)
				VALUES('asset',$1,$2,$3) RETURNING id`, ownerID, assetID, sess.UserID).Scan(&requestID)
		}
		if err == nil {
			err = insertAudit(r.Context(), tx, sess.UserID, "asset.delete_requested", "asset", assetID.String(), requestIDFromContext(r), map[string]any{"deletion_id": requestID})
		}
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_DELETE_FAILED", "删除资产失败", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": requestID, "status": "pending"})
}

func (s *Server) getDeletion(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	sess := currentSession(r)
	var kind, status string
	var errorCode, errorMessage *string
	err := s.db.QueryRow(r.Context(), `SELECT kind,status,error_code,error_message FROM deletion_requests
		WHERE id=$1 AND (owner_user_id=$2 OR $3='admin')`, id, sess.UserID, sess.Role).Scan(&kind, &status, &errorCode, &errorMessage)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "DELETION_NOT_FOUND", "删除任务不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取删除状态失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "kind": kind, "status": status, "error_code": errorCode, "error_message": errorMessage})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	actor := currentSession(r)
	if id == actor.UserID {
		writeError(w, http.StatusConflict, "SELF_DELETE_PROTECTED", "不能删除当前登录的管理员", false, r)
		return
	}
	var input struct {
		Username string `json:"username"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "删除用户失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock($1)`, int64(4919348247202)); err != nil {
		writeError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "删除用户失败", true, r)
		return
	}
	var username, role, status string
	err = tx.QueryRow(r.Context(), `SELECT username::text,role,status FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&username, &role, &status)
	if isNotFound(err) || status == "deleted" {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "用户不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "删除用户失败", true, r)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(input.Username), username) {
		writeError(w, http.StatusUnprocessableEntity, "USERNAME_CONFIRMATION_MISMATCH", "输入的用户名与目标用户不一致", false, r)
		return
	}
	if role == "admin" && status == "active" {
		var admins int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE role='admin' AND status='active'`).Scan(&admins); err != nil || admins <= 1 {
			writeError(w, http.StatusConflict, "LAST_ADMIN_PROTECTED", "不能删除最后一名有效管理员", false, r)
			return
		}
	}
	var requestID uuid.UUID
	if status == "deleting" {
		err = tx.QueryRow(r.Context(), `SELECT id FROM deletion_requests WHERE target_user_id=$1 AND status IN ('pending','running')`, id).Scan(&requestID)
		if isNotFound(err) {
			err = tx.QueryRow(r.Context(), `INSERT INTO deletion_requests(kind,owner_user_id,target_user_id,requested_by)
				VALUES('user',$1,$1,$2) RETURNING id`, id, actor.UserID).Scan(&requestID)
		}
	} else {
		_, err = tx.Exec(r.Context(), `UPDATE users SET status='deleting',session_version=session_version+1,updated_at=now() WHERE id=$1`, id)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE generation_jobs SET status='cancelled',dispatch_state='finished',cancel_mode='local',completed_at=now(),updated_at=now()
				WHERE owner_user_id=$1 AND status='queued'`, id)
		}
		if err == nil {
			rows, queryErr := tx.Query(r.Context(), `UPDATE generation_jobs SET status='cancelling',
				cancel_mode=CASE WHEN provider_job_id IS NULL THEN 'discard_result_only' ELSE 'requested_upstream' END,updated_at=now()
				WHERE owner_user_id=$1 AND status IN ('dispatched','submitting','submission_uncertain','provider_pending','ingesting') RETURNING id`, id)
			err = queryErr
			if queryErr == nil {
				jobIDs := make([]uuid.UUID, 0)
				for rows.Next() {
					var jobID uuid.UUID
					if rows.Scan(&jobID) == nil {
						jobIDs = append(jobIDs, jobID)
					}
				}
				rows.Close()
				for _, jobID := range jobIDs {
					_, _ = tx.Exec(r.Context(), `SELECT pg_notify('job_controls',$1)`, jobID.String())
				}
			}
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO deletion_requests(kind,owner_user_id,target_user_id,requested_by)
				VALUES('user',$1,$1,$2) RETURNING id`, id, actor.UserID).Scan(&requestID)
		}
		if err == nil {
			err = insertAudit(r.Context(), tx, actor.UserID, "user.delete_requested", "user", id.String(), requestIDFromContext(r), map[string]any{"deletion_id": requestID})
		}
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "删除用户失败", true, r)
		return
	}
	s.sessions.Clear()
	s.hub.invalidate(id)
	writeJSON(w, http.StatusAccepted, map[string]any{"id": requestID, "status": "pending"})
}
