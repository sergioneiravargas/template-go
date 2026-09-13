package example

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/sergioneiravargas/template-go/internal/auth"
	"github.com/sergioneiravargas/template-go/internal/platform/log"

	"github.com/go-chi/chi/v5"
)

type errorResponse struct {
	Error string `json:"error"`
}

func httpError(
	w http.ResponseWriter,
	message string,
	status int,
) {
	response := errorResponse{
		Error: message,
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	http.Error(w, string(responseJSON), status)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func HelloWorldAPIHandler(logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userInfo, found := auth.UserInfoFromRequest(r)
		if !found {
			httpError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		logger.Debug("HTTP route reached", log.Context{
			"route_path": r.URL.Path,
			"user_id":    userInfo.ID,
		})

		writeJSON(w, http.StatusOK, map[string]string{
			"message": fmt.Sprintf("Hello, %s!", userInfo.ID),
		})
	}
}

func CreateMessageAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, found := auth.UserInfoFromRequest(r); !found {
			httpError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var input CreateMessageInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			logger.Error("Failed to decode create message body", log.Context{"error": err.Error()})
			httpError(w, "Bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		entry, err := service.CreateMessage(ctx, input)
		if err != nil {
			logger.Error("Failed to create message", log.Context{"error": err.Error()})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, entry)
	}
}

func GetMessageAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, found := auth.UserInfoFromRequest(r); !found {
			httpError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			httpError(w, "Bad request", http.StatusBadRequest)
			return
		}

		entry, err := service.GetMessage(ctx, id)
		if err != nil {
			if errors.Is(err, ErrMessageNotFound) {
				httpError(w, "Not found", http.StatusNotFound)
				return
			}
			logger.Error("Failed to get message", log.Context{"error": err.Error(), "id": id})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, entry)
	}
}

func BroadcastRoomAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userInfo, found := auth.UserInfoFromRequest(r)
		if !found {
			httpError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var input BroadcastRoomInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			logger.Error("Failed to decode broadcast room body", log.Context{"error": err.Error()})
			httpError(w, "Bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// Identity and room always come from trusted sources, never the body.
		input.Room = chi.URLParam(r, "room")
		input.UserID = userInfo.ID

		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := service.BroadcastToRoom(ctx, input); err != nil {
			logger.Error("Failed to broadcast to room", log.Context{"error": err.Error(), "room": input.Room})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
	}
}
