package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// The user information contained in the OIDC claims
type UserInfo struct {
	ID            string `json:"sub"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

func UserInfoFromClaims(claims MapClaims) (*UserInfo, error) {
	var userInfo UserInfo

	sub, valid := claims["sub"].(string)
	if !valid {
		return nil, fmt.Errorf("invalid claim sub")
	}
	userInfo.ID = sub

	givenName, valid := claims["given_name"].(string)
	if valid {
		userInfo.GivenName = givenName
	}

	familyName, valid := claims["family_name"].(string)
	if valid {
		userInfo.FamilyName = familyName
	}

	email, valid := claims["email"].(string)
	if valid {
		userInfo.Email = email
	}

	emailVerified, _ := claims["email_verified"].(bool)
	if valid {
		userInfo.EmailVerified = emailVerified
	}

	return &userInfo, nil
}

// Fetches UserInfo from the given URL
func FetchUserInfo(
	url string,
	accessToken string,
) (*UserInfo, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	httpClient := &http.Client{}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var userInfo UserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}
