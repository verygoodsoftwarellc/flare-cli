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

type Metric struct {
	Operation string  `json:"operation,omitempty"`
	Impact    float64 `json:"impact"`
	Count     int64   `json:"count"`
	Sum       int64   `json:"sum"`
	Avg       float64 `json:"avg"`
	ErrorRate float64 `json:"error_rate"`
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

type Overview struct {
	Environment Environment `json:"environment"`
	Hours       int         `json:"hours"`
	AsOf        string      `json:"as_of"`
	DataThrough *string     `json:"data_through"`
	Namespaces  []Namespace `json:"namespaces"`
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
	Operations  []Metric    `json:"operations"`
	Pagination  Pagination  `json:"pagination"`
}
