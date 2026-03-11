// Package status is used to allow sorting of errors to http status codes.
package status

import (
	"errors"
	"fmt"
	"net/http"
)

var HTTP = map[int]error{
	http.StatusBadRequest:      errors.New("'bad request'"),
	http.StatusUnauthorized:    errors.New("'unauthorized'"),
	http.StatusForbidden:       errors.New("'forbidden'"),
	http.StatusNotFound:        errors.New("'not found'"),
	http.StatusTooManyRequests: errors.New("'too many requests'"),

	http.StatusInternalServerError: errors.New("'internal server error'"),
	http.StatusServiceUnavailable:  errors.New("'service unavailable'"),
}

// ErrToCode returns a http code for an error.
//
// Works only if err is wrapped HTTP Error. Otherwise -1 is returned.
// If an error consists of more wrapped HTTP errors, only one is returned but not deterministically.
func ErrToCode(err error) int {
	for j := range HTTP {
		if errors.Is(err, HTTP[j]) {
			return j
		}
	}
	return -1
}

// Add adds http error at the beginning of the error.
func Add(err error, code int) error {
	return fmt.Errorf("%w: %w", HTTP[code], err)
}
