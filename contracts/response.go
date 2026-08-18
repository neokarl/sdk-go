// Package contracts holds the wire-format types the platform exposes to
// plugins and the frontend. These are the public surface — change carefully.
package contracts

// Envelope is the canonical response shape for every API endpoint.
//
// The platform API standard:
//
//	{ "success": true, "data": ..., "error": null }
type Envelope struct {
	Success bool       `json:"success"`
	Data    any        `json:"data,omitempty"`
	Error   *ErrorBody `json:"error,omitempty"`
}

// ErrorBody is the public-facing error projection. The internal cause stays
// in the server logs; only Code/Message/Details cross the wire.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Ok returns a success envelope wrapping data.
func Ok(data any) Envelope {
	return Envelope{Success: true, Data: data}
}

// Fail returns an error envelope.
func Fail(code, message string, details map[string]any) Envelope {
	return Envelope{Success: false, Error: &ErrorBody{Code: code, Message: message, Details: details}}
}
