package webui

// The tray job endpoints (#164): the tray app takes the service's
// user-session actions here and posts the replies back (package trayexec
// on the tray side, session.Broker behind Deps.Trays).
//
//	POST /api/tray/poll    → 200 {"job":{"id","args"}} | 204 nothing within ~25s
//	POST /api/tray/result  {"id","result"} → 200 | 404 unknown_job
//	POST /api/tray/quit    → 200 (the user quit the tray app, #167)
//
// Both sit behind apiAuth like every call of the tray app (its session
// cookie when a secret is set, and the CSRF header), and on top of that
// every request is traced to the process that opened the connection
// (peer.go): it must be this service's own exe, run by the logged-on user
// of an interactive session (trustTrayPeer). That process's session is
// the one the poll takes jobs for, and the one a reply must come from;
// nothing in the request can name another. No administrator is required,
// unlike the local login: a standard user's tray runs the jobs as that
// user in their own session, which is where the service ran them before.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Protomothis/smartthings-pc-control/internal/httpx"
	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/trayexec"
)

// trayResultMaxBody caps a result post: one reply line, the longest being
// `media info` with four short texts.
const trayResultMaxBody = 32 << 10

// identifyTrayPeer finds the process behind r and checks that it is this
// app's tray in an interactive session.
func (s *Server) identifyTrayPeer(r *http.Request) (peerProcess, error) {
	p, err := s.findLocalPeer(r)
	if err != nil {
		return p, err
	}
	return p, trustTrayPeer(p, s.peerSameExe)
}

// trayPeer is identifyTrayPeer that answers the refusal itself: 403
// {"error":"not_tray","code":…}. ok is false when it did.
func (s *Server) trayPeer(w http.ResponseWriter, r *http.Request) (peerProcess, bool) {
	p, err := s.identifyTrayPeer(r)
	if err == nil {
		return p, true
	}
	code := "unknown_peer"
	var te *localTrustError
	if errors.As(err, &te) {
		code = te.code
	}
	msg := err.Error()
	s.local.mu.Lock()
	repeat := msg == s.local.lastTrayRefusal
	s.local.lastTrayRefusal = msg
	s.local.mu.Unlock()
	if !repeat {
		logx.Printf("Tray job request refused: %s", msg)
	}
	httpx.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not_tray", "code": code})
	return p, false
}

// serveTrayPoll serves POST /api/tray/poll: the next job for the caller's
// session, or 204 when none came within the poll window. The poll ends
// early when the tray app goes away (the request's context).
func (s *Server) serveTrayPoll(w http.ResponseWriter, r *http.Request) {
	p, ok := s.trayPeer(w, r)
	if !ok {
		return
	}
	job, ok := s.d.Trays.Poll(r.Context(), p.SessionID, p.PID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, trayexec.PollReply{Job: &trayexec.Job{ID: job.ID, Args: job.Args}})
}

// serveTrayResult serves POST /api/tray/result: the reply line of a job
// handed to the caller's session. 404 unknown_job when nobody waits for
// it any more (it timed out) or it was another session's.
func (s *Server) serveTrayResult(w http.ResponseWriter, r *http.Request) {
	p, ok := s.trayPeer(w, r)
	if !ok {
		return
	}
	var body trayexec.ResultBody
	if err := json.NewDecoder(io.LimitReader(r.Body, trayResultMaxBody)).Decode(&body); err != nil || body.ID == "" || len(body.Result) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid result")
		return
	}
	if err := s.d.Trays.Reply(p.SessionID, body.ID, body.Result); err != nil {
		if errors.Is(err, session.ErrUnknownJob) {
			httpx.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_job"})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// serveTrayQuit serves POST /api/tray/quit: the caller's tray app is
// quitting because its user chose 종료 (#167). The service then starts no
// tray app in that session on its own until one runs there again. Like
// the poll, the session is the calling process's, never the request's.
func (s *Server) serveTrayQuit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.trayPeer(w, r)
	if !ok {
		return
	}
	s.d.Trays.Quit(p.SessionID)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
