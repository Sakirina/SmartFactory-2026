package api

import (
	"crypto/subtle"
	"net/http"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/model"
)

func (s *Server) nativeAlarmUpdate(w http.ResponseWriter, r *http.Request) {
	if s.ServiceToken == "" || subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(s.ServiceToken)) != 1 {
		fail(w, identity.ErrAuthentication)
		return
	}
	var input model.NativeAlarmUpdate
	if err := decode(r, &input); err != nil {
		fail(w, err)
		return
	}
	out, err := s.BusinessApplication().UpdateNativeAlarm(r.Context(), r.PathValue("id"), input)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
