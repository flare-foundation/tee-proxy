// Package status is used to allow sorting of errors to http status codes.
package status

import (
	"errors"
	"net/http"
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
func ErrToCode(err error) int {
	for j := range HTTP {
		if errors.Is(err, HTTP[j]) {
			return j
		}
	}
	return -1
}

// HandleError replies to unsuccessful request.
// If error is wrapped HTTP error, status is retrieved, and error is given in response.
// Otherwise, status 500 and "internal server error" is given in the reply.
func HandleError(w http.ResponseWriter, err error) {
	code := ErrToCode(err)
	reason := err.Error()
	if code == -1 {
		code = http.StatusInternalServerError
		reason = "internal server error"
	}

	http.Error(w, reason, code)
}
