package slack

// ErrorCode is Slack's error code ("not_in_channel"). It is not Code():
// that is the field's name, and Go does not let a method share it.
func (e *APIError) ErrorCode() string { return e.Code }
