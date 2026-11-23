package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// APIError is a failed answer of the API.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Field names the request field a domain error is about (error.field), if any.
	Field string
	// Fields holds per-field validation messages (Laravel "errors" or error.details).
	Fields map[string][]string
	Method string
	Path   string
}

func (e *APIError) Error() string {
	var b strings.Builder
	if e.Method != "" {
		fmt.Fprintf(&b, "%s %s: ", e.Method, e.Path)
	}
	fmt.Fprintf(&b, "HTTP %d", e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if len(e.Fields) > 0 {
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "; %s: %s", k, strings.Join(e.Fields[k], " "))
		}
	}
	return b.String()
}

// IsNotFound reports whether err is a 404 of the API.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// IsBusy reports whether the API refused an action because another one is still running on the
// resource. Such a request can be sent again once the other action has finished.
func IsBusy(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "server_busy", "task_already_running", "lock_timeout", "cluster_busy", "volume_busy":
		return true
	}
	return false
}

// AsAPIError returns the APIError inside err.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	ok := errors.As(err, &apiErr)
	return apiErr, ok
}

type errorBody struct {
	Success *bool           `json:"success"`
	Message string          `json:"message"`
	Errors  json.RawMessage `json:"errors"`
	Error   json.RawMessage `json:"error"`
}

type errorObject struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Field   string          `json:"field"`
	Details json.RawMessage `json:"details"`
}

func parseError(status int, raw []byte) *APIError {
	apiErr := &APIError{Status: status}
	var body errorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		apiErr.Message = strings.TrimSpace(truncate(string(raw), 300))
		return apiErr
	}

	apiErr.Message = body.Message
	apiErr.Fields = fieldMessages(body.Errors)

	if len(body.Error) > 0 {
		var obj errorObject
		if json.Unmarshal(body.Error, &obj) == nil {
			apiErr.Code = obj.Code
			if obj.Message != "" {
				apiErr.Message = obj.Message
			}
			apiErr.Field = obj.Field
			if details := fieldMessages(obj.Details); len(details) > 0 {
				apiErr.Fields = details
			}
		} else {
			var text string
			if json.Unmarshal(body.Error, &text) == nil {
				apiErr.Message = text
			}
		}
	}
	if apiErr.Code == "" && status == http.StatusUnprocessableEntity && len(apiErr.Fields) > 0 {
		apiErr.Code = "validation_error"
	}
	return apiErr
}

// fieldMessages reads {"field": ["msg", ...]} or {"field": "msg"}; anything else is ignored.
func fieldMessages(raw json.RawMessage) map[string][]string {
	if len(raw) == 0 {
		return nil
	}
	var generic map[string]any
	if json.Unmarshal(raw, &generic) != nil {
		return nil
	}
	out := map[string][]string{}
	for key, value := range generic {
		switch v := value.(type) {
		case string:
			out[key] = []string{v}
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					out[key] = append(out[key], s)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
