package account

import "backend/internal/id"

func newID() (string, error) {
	return id.New("acc_")
}

func newToken() (string, error) {
	return id.Secret("at_")
}
