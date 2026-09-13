package validation

import (
	"errors"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Basic phone regex: optional +, followed by 7 to 15 digits.
	// This is a loose validation to support international numbers.
	phoneRegex = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
)

func ValidateEmail(email string) error {
	if email == "" {
		return errors.New("email cannot be empty")
	}
	_, err := mail.ParseAddress(email)
	if err != nil {
		return errors.New("invalid email format")
	}
	return nil
}

func ValidatePhone(phone string) error {
	if phone == "" {
		return errors.New("phone cannot be empty")
	}
	if !phoneRegex.MatchString(phone) {
		return errors.New("invalid phone format")
	}
	return nil
}

var (
	ErrInvalidRUTFormat = errors.New("invalid RUT format")
	ErrInvalidRUTDigit  = errors.New("invalid RUT check digit")
)

// ValidateRUT checks if a Chilean RUT (Rol Único Tributario) is valid.
// It cleans the input (removing dots and hyphens) before validation.
// It returns an error if the RUT is invalid, or nil if it is valid.
func ValidateRUT(rut string) error {
	// Clean the RUT
	cleanRUT := strings.ReplaceAll(rut, ".", "")
	cleanRUT = strings.ReplaceAll(cleanRUT, "-", "")
	cleanRUT = strings.ToUpper(strings.TrimSpace(cleanRUT))

	// Basic format check (at least 2 characters: 1 digit + check digit)
	if len(cleanRUT) < 2 {
		return ErrInvalidRUTFormat
	}

	// Validate format using regex (digits + K)
	matched, _ := regexp.MatchString(`^[0-9]+[0-9K]$`, cleanRUT)
	if !matched {
		return ErrInvalidRUTFormat
	}

	// Split body and check digit
	body := cleanRUT[:len(cleanRUT)-1]
	dv := cleanRUT[len(cleanRUT)-1:]

	// Calculate check digit
	sum := 0
	multiplier := 2

	// Reverse iterate over body
	for i := len(body) - 1; i >= 0; i-- {
		digit, _ := strconv.Atoi(string(body[i]))
		sum += digit * multiplier
		multiplier++
		if multiplier > 7 {
			multiplier = 2
		}
	}

	expectedDVCode := 11 - (sum % 11)
	var expectedDV string
	if expectedDVCode == 11 {
		expectedDV = "0"
	} else if expectedDVCode == 10 {
		expectedDV = "K"
	} else {
		expectedDV = strconv.Itoa(expectedDVCode)
	}

	if dv != expectedDV {
		return ErrInvalidRUTDigit
	}

	return nil
}
