package api

type Pagination struct {
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
}

type Organization struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Project struct {
	ID             int64  `json:"id"`
	OrganizationID int64  `json:"organization_id"`
	Name           string `json:"name"`
}

type Environment struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
}

// Selector identifies one operation (or one group of a generic query)
// precisely enough to pass back as a filter. Blank parts are omitted by the
// server.
type Selector struct {
	Namespace string `json:"namespace,omitempty"`
	Service   string `json:"service,omitempty"`
	Target    string `json:"target,omitempty"`
	Operation string `json:"operation,omitempty"`
}

type Metric struct {
	Operation string    `json:"operation,omitempty"`
	Selector  *Selector `json:"selector,omitempty"`
	Impact    float64   `json:"impact"`
	Count     int64     `json:"count"`
	Sum       int64     `json:"sum"`
	Avg       float64   `json:"avg"`
	ErrorRate float64   `json:"error_rate"`
}

type Namespace struct {
	Namespace  string   `json:"namespace"`
	Impact     float64  `json:"impact"`
	Count      int64    `json:"count"`
	Sum        int64    `json:"sum"`
	Avg        float64  `json:"avg"`
	ErrorRate  float64  `json:"error_rate"`
	Operations []Metric `json:"operations"`
}

// Window describes the time range a metrics response covers. Bucket is
// minute (1h/6h), hour (24h-168h), or day (672h).
type Window struct {
	Hours       int    `json:"hours"`
	Bucket      string `json:"bucket,omitempty"`
	AsOf        string `json:"as_of"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
}

type Overview struct {
	Environment Environment `json:"environment"`
	Window
	Changes     []Change    `json:"changes,omitempty"`
	DataThrough *string     `json:"data_through"`
	Namespaces  []Namespace `json:"namespaces"`
}

// Stats are totals over a window: sum and avg in milliseconds, error_rate a percentage.
type Stats struct {
	Count      int64   `json:"count"`
	Sum        int64   `json:"sum"`
	Avg        float64 `json:"avg"`
	ErrorCount int64   `json:"error_count"`
	ErrorRate  float64 `json:"error_rate"`
	// Impact is present only where the server reports it (groups and the
	// namespace total); a pointer keeps a reported zero distinct from absent.
	Impact *float64 `json:"impact,omitempty"`
}

// Series holds one value per entry of the response's Buckets, zero-filled.
// Per-bucket avg is Sum[i]/Count[i]; per-bucket error rate is ErrorCount[i]/Count[i].
type Series struct {
	Count      []int64 `json:"count"`
	Sum        []int64 `json:"sum"`
	ErrorCount []int64 `json:"error_count"`
}

// SeriesEnvelope is shared by every series-shaped response. Buckets lists the
// bucket start times every Series in the response is aligned to.
type SeriesEnvelope struct {
	Environment Environment `json:"environment"`
	Namespace   string      `json:"namespace"`
	Window
	Buckets     []string `json:"buckets"`
	DataThrough *string  `json:"data_through"`
	Stats       Stats    `json:"stats"`
	Series      Series   `json:"series"`
}

type OperationSeriesResponse struct {
	SeriesEnvelope
	Selector Selector `json:"selector"`
	Changes  []Change `json:"changes,omitempty"`
}

type RankedOperation struct {
	Operation string    `json:"operation"`
	Selector  *Selector `json:"selector,omitempty"`
	Stats
	Series Series `json:"series"`
}

// QueryTotal is the filtered set's totals, with a series when requested.
type QueryTotal struct {
	Stats
	Series *Series `json:"series,omitempty"`
}

type QueryGroup struct {
	Selector Selector `json:"selector"`
	Label    string   `json:"label"`
	Stats
	Series *Series `json:"series,omitempty"`
}

// QueryResponse is the generic GET /api/v1/metrics/query result. Buckets is
// present only when series were requested.
type QueryResponse struct {
	Environment Environment `json:"environment"`
	Window
	Buckets     []string     `json:"buckets,omitempty"`
	Filter      Selector     `json:"filter"`
	GroupBy     []string     `json:"group_by"`
	Sort        string       `json:"sort"`
	Direction   string       `json:"direction"`
	Limit       int          `json:"limit"`
	MinCount    int          `json:"min_count"`
	DataThrough *string      `json:"data_through"`
	Total       QueryTotal   `json:"total"`
	Groups      []QueryGroup `json:"groups"`
}

type NamespaceOverviewResponse struct {
	SeriesEnvelope
	TopBySum   []RankedOperation `json:"top_by_sum"`
	TopByCount []RankedOperation `json:"top_by_count"`
}

type OrganizationsResponse struct {
	Organizations []Organization `json:"organizations"`
	Pagination    Pagination     `json:"pagination"`
}

type ProjectsResponse struct {
	Projects   []Project  `json:"projects"`
	Pagination Pagination `json:"pagination"`
}

type EnvironmentsResponse struct {
	Environments []Environment `json:"environments"`
	Pagination   Pagination    `json:"pagination"`
}

type OverviewsResponse struct {
	Overviews []Overview `json:"overviews"`
}

type NamespaceResponse struct {
	Environment Environment `json:"environment"`
	Namespace   string      `json:"namespace"`
	Window
	Operations []Metric   `json:"operations"`
	Pagination Pagination `json:"pagination"`
}

// Change is a deploy detected from the revision the Flare gem reports.
// FinishedAt and RolloutSeconds are null while it is still rolling out, and
// Bucket, the start of the series bucket it began in, is null when it began
// before the window.
type Change struct {
	ID               int64    `json:"id"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title"`
	Revision         string   `json:"revision"`
	PreviousRevision *string  `json:"previous_revision"`
	Rollback         bool     `json:"rollback"`
	OccurredAt       string   `json:"occurred_at"`
	FinishedAt       *string  `json:"finished_at"`
	RolloutSeconds   *float64 `json:"rollout_seconds"`
	Bucket           *string  `json:"bucket"`
}

type ChangesResponse struct {
	Environment Environment `json:"environment"`
	Window
	Changes []Change `json:"changes"`
}

type TraceSummary struct {
	ID            int64   `json:"id"`
	EnvironmentID int64   `json:"environment_id"`
	RootName      string  `json:"root_name"`
	StartedAt     string  `json:"started_at"`
	DurationMS    float64 `json:"duration_ms"`
	SpanCount     int64   `json:"span_count"`
	Rule          *string `json:"rule"`
}

// TracesResponse pages newest first, so Pagination.Previous is always null.
type TracesResponse struct {
	Environment Environment    `json:"environment"`
	Traces      []TraceSummary `json:"traces"`
	Pagination  Pagination     `json:"pagination"`
}

// Span is one span of a trace, in tree order. Count is set when repeated
// siblings were collapsed into it, and DurationMS is then their total.
type Span struct {
	ID         string  `json:"id"`
	ParentID   *string `json:"parent_id,omitempty"`
	Depth      int     `json:"depth"`
	Name       string  `json:"name"`
	Label      string  `json:"label,omitempty"`
	Kind       string  `json:"kind"`
	OffsetMS   float64 `json:"offset_ms"`
	DurationMS float64 `json:"duration_ms"`
	Source     string  `json:"source,omitempty"`
	Count      int     `json:"count,omitempty"`
}

// TraceResponse is one trace and its spans. Available is false, with a
// Message and no spans, until the spans arrive.
type TraceResponse struct {
	Trace     TraceSummary `json:"trace"`
	Available bool         `json:"available"`
	Message   string       `json:"message,omitempty"`
	Collapsed bool         `json:"collapsed"`
	Truncated bool         `json:"truncated"`
	Spans     []Span       `json:"spans"`
}
