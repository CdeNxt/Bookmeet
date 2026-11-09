package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var bookingPlacesClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (h *Handler) SearchBookingPlaces(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(os.Getenv("BOOKMEET_GOOGLE_PLACES_KEY"))
	if key == "" {
		h.writeError(w, http.StatusNotFound, "place search is unavailable")
		return
	}
	var enabled bool
	err := h.db.QueryRowContext(r.Context(), `SELECT allow_in_person FROM event_types
  WHERE slug = ? AND is_active = 1 AND is_public = 1`, r.PathValue("slug")).Scan(&enabled)
	if err != nil || !enabled {
		h.writeError(w, http.StatusNotFound, "place search is unavailable")
		return
	}
	var in struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid place query")
		return
	}
	in.Query = strings.TrimSpace(in.Query)
	if len(in.Query) < 3 || len(in.Query) > 500 {
		h.writeError(w, http.StatusBadRequest, "invalid place query")
		return
	}
	places, err := searchGooglePlaces(r.Context(), key, in.Query)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "place search is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeJSON(w, http.StatusOK, places)
}

func searchGooglePlaces(ctx context.Context, key, query string) ([]string, error) {
	body, err := json.Marshal(map[string]any{"textQuery": query, "pageSize": 5})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://places.googleapis.com/v1/places:searchText", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("X-Goog-FieldMask", "places.displayName,places.formattedAddress")
	res, err := bookingPlacesClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google Places returned status %d", res.StatusCode)
	}
	var data struct {
		Places []struct {
			DisplayName struct {
				Text string `json:"text"`
			} `json:"displayName"`
			FormattedAddress string `json:"formattedAddress"`
		} `json:"places"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&data); err != nil {
		return nil, err
	}
	places := make([]string, 0, len(data.Places))
	for _, place := range data.Places {
		label := place.FormattedAddress
		if place.DisplayName.Text != "" && !strings.EqualFold(place.DisplayName.Text, label) {
			if label != "" {
				label = place.DisplayName.Text + ", " + label
			} else {
				label = place.DisplayName.Text
			}
		}
		if label != "" && len(label) <= 500 {
			places = append(places, label)
		}
	}
	return places, nil
}
