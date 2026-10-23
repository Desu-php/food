package httpadapter

import (
	"net/http"

	"github.com/Desu-php/food/platform/security"
)

// profileDTO is an explicit public allowlist, independent of registry fields.
type profileDTO struct {
	UserID        int64    `json:"user_id"`
	Roles         []string `json:"roles"`
	RestaurantIDs []int64  `json:"restaurant_ids"`
	Permissions   []string `json:"permissions"`
}

func (h *handler) profile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := security.FromContext(r.Context())
	if !ok || p.UserID <= 0 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		h.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	h.writeJSON(w, http.StatusOK, profileDTO{
		UserID: p.UserID, Roles: append([]string{}, p.Roles...),
		RestaurantIDs: append([]int64{}, p.Restaurants...), Permissions: append([]string{}, p.Permissions...),
	})
}
