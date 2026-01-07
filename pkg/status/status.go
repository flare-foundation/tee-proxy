// Package status is used to allow sorting of errors to http status codes.
package status

import (
	"errors"
	"fmt"
)

var HTTP = map[int]error{
	400: errors.New("'bad request'"),
	403: errors.New("'forbidden'"),
	404: errors.New("'not found'"),
	429: errors.New("'too many requests'"),

	500: errors.New("'internal server error'"),
	503: errors.New("'service unavailable'"),
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
