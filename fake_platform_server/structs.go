package fake_platform_server

type JobState string

const (
	Pending    JobState = "Pending"
	InProgress JobState = "InProgress"
	Finished   JobState = "Finished"
)

type RequestType string

const (
	ListJob      RequestType = "ListJob"
	ListAgent    RequestType = "ListAgent"
	CreateAgent  RequestType = "CreateAgent"
	ReplaceAgent RequestType = "ReplaceAgent"
	DeleteAgent  RequestType = "DeleteAgent"
	GetPoolId    RequestType = "GetPoolId"
	AssignJob    RequestType = "AssignJob"
	FinishJob    RequestType = "FinishJob"
)

type Job struct {
	ID          int
	PoolID      int
	State       JobState
	Duration    int64
	StartDelay  int64
	FinishDelay int64
	Demands     []string
}

type Request struct {
	Type              RequestType
	AgentName         string
	AgentCapabilities []string
	PoolID            int
	JobID             int
}

type Agent struct {
	Name string
	ID   int
}
