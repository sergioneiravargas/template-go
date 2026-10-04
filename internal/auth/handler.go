package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
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

func RegisterAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var input RegisterInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		user, err := service.Register(ctx, input)
		if err != nil {
			if errors.Is(err, ErrEmailAlreadyExists) {
				httpError(w, "Email already registered", http.StatusConflict)
				return
			}
			logger.Error("Failed to register user", log.Context{"error": err.Error()})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, user)
	}
}

func LoginAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var input LoginInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		pair, err := service.Login(ctx, input)
		if err != nil {
			if errors.Is(err, ErrInvalidCredentials) {
				httpError(w, "Invalid credentials", http.StatusUnauthorized)
				return
			}
			logger.Error("Failed to log user in", log.Context{"error": err.Error()})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, pair)
	}
}

func RefreshAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var input RefreshInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		pair, err := service.Refresh(ctx, input)
		if err != nil {
			if errors.Is(err, ErrInvalidRefreshToken) {
				httpError(w, "Invalid refresh token", http.StatusUnauthorized)
				return
			}
			logger.Error("Failed to refresh tokens", log.Context{"error": err.Error()})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, pair)
	}
}

// ForgotPasswordAPIHandler always answers 202 for valid input so the
// response does not reveal whether an account exists; the reset email is
// sent asynchronously by the worker.
func ForgotPasswordAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var input ForgotPasswordInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := service.ForgotPassword(ctx, input); err != nil {
			logger.Error("Failed to process forgot password request", log.Context{"error": err.Error()})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	}
}

func ResetPasswordAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var input ResetPasswordInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := service.ResetPassword(ctx, input); err != nil {
			if errors.Is(err, ErrInvalidPasswordResetToken) {
				httpError(w, "Invalid password reset token", http.StatusBadRequest)
				return
			}
			logger.Error("Failed to reset password", log.Context{"error": err.Error()})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func LogoutAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var input LogoutInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if err := input.Validate(); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := service.Logout(ctx, input); err != nil {
			logger.Error("Failed to log user out", log.Context{"error": err.Error()})
			httpError(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
