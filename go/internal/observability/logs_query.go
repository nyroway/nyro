package observability

// LogQuery filters and paginates the admin request-audit projection.
type LogQuery struct {
	Limit      int64
	Offset     int64
	UpstreamID string
	RouteID    string
	RouteModel string
	ConsumerID string
	StatusMin  *int32
	StatusMax  *int32
}

// LogPage contains a page of matching records and the total before pagination.
type LogPage struct {
	Items []LogRecord `json:"items"`
	Total int64       `json:"total"`
}
