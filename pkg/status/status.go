// Package status is used to allow sorting of errors to http status codes.
package status

import "errors"

var HTTP = map[int]error{
	400: errors.New("'bad request'"),
	403: errors.New("'forbidden'"),
	404: errors.New("'not found'"),
	429: errors.New("'too many requests'"),

	500: errors.New("'internal server error'"),
}

// ErrToCode returns a http code for an error.
//
// Works only if err is wrapped HTTPError. Otherwise -1 is returned.
func ErrToCode(err error) int {
	for j := range HTTP {
		if errors.Is(err, HTTP[j]) {
			return j
		}
	}
	return -1
}
