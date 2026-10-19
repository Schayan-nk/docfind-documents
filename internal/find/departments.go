package find

import (
	"errors"
	"net/http"

	"github.com/Schayan-nk/docfind-documents/internal/auth"
)

// The users page's departments (asked for on 1405/07/12): imported from the
// office-automation directory (HumanResources.Department, the posts people
// hold through EmployeePosition) and then docfind's to create, rename, move
// and delete; each person in any number of them, one primary. And the
// profile fields the users page edits. All for whoever manages accounts.

func actor(r *http.Request) string {
	if me := auth.UserFrom(r); me != nil {
		return me.Username
	}
	return "?"
}

// handleUserProfile is PUT /api/users/profile: a person's names, addresses,
// personnel code, and whether their next sign-in asks for a new password.
func (s *Server) handleUserProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		auth.Profile
	}
	if err := readJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, `{"error":"درخواست نامعتبر است."}`)
		return
	}
	if err := s.guard.Users.SetProfile(req.Username, req.Profile); err != nil {
		if errors.Is(err, auth.ErrNoSuchUser) {
			writeUserError(w, err)
			return
		}
		writeJSONError(w, http.StatusBadRequest, `{"error":`+jsonQuote("ذخیره نشد: "+err.Error())+`}`)
		return
	}
	s.auditChange(actor(r), "user.profile", "user:"+req.Username, nil)
	u, _ := s.guard.Users.Get(req.Username)
	writeJSON(w, map[string]any{"ok": true, "user": u.Public()})
}
