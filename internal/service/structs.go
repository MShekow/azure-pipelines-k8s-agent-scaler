package service

import (
	corev1 "k8s.io/api/core/v1"
	"reflect"
	"strings"
	"time"
)

type AzurePipelinesApiPoolNameResponse struct {
	// Because there could be multiple Azure Pipeline pools with the same name, the API returns an array. The objects
	// have many more fields, but we only care about the ID, and omit defining the other fields.
	Value []struct {
		ID int `json:"id"`
	} `json:"value"`
}

type AzurePipelinesAgentList struct {
	Count int                   `json:"count"`
	Value []AzurePipelinesAgent `json:"value"`
}

type AzurePipelinesAgent struct {
	CreatedOn time.Time `json:"createdOn"`
	Name      string    `json:"name"`
	Id        int       `json:"id"`
	Status    string    `json:"status"`
}

type AzurePipelinesRegisterAgentRequest struct {
	Name               string            `json:"name"`
	Version            string            `json:"version"`
	OsDescription      string            `json:"osDescription"`
	Enabled            bool              `json:"enabled"`
	Status             string            `json:"status"`
	ProvisioningState  string            `json:"provisioningState"`
	SystemCapabilities map[string]string `json:"systemCapabilities"`
}

type AzurePipelinesApiJobRequests struct {
	Count int                           `json:"count"`
	Value []AzurePipelinesApiJobRequest `json:"value"`
}

type AzurePipelinesApiJobRequest struct {
	RequestID     int       `json:"requestId"`
	QueueTime     time.Time `json:"queueTime"`
	AssignTime    time.Time `json:"assignTime,omitempty"`
	ReceiveTime   time.Time `json:"receiveTime,omitempty"`
	LockedUntil   time.Time `json:"lockedUntil,omitempty"`
	ServiceOwner  string    `json:"serviceOwner"`
	HostID        string    `json:"hostId"`
	Result        *string   `json:"result"`
	ScopeID       string    `json:"scopeId"`
	PlanType      string    `json:"planType"`
	PlanID        string    `json:"planId"`
	JobID         string    `json:"jobId"`
	Demands       []string  `json:"demands"`
	ReservedAgent *struct {
		Links struct {
			Self struct {
				Href string `json:"href"`
			} `json:"self"`
			Web struct {
				Href string `json:"href"`
			} `json:"web"`
		} `json:"_links"`
		ID                int    `json:"id"`
		Name              string `json:"name"`
		Version           string `json:"version"`
		OsDescription     string `json:"osDescription"`
		Enabled           bool   `json:"enabled"`
		Status            string `json:"status"`
		ProvisioningState string `json:"provisioningState"`
		AccessPoint       string `json:"accessPoint"`
	} `json:"reservedAgent,omitempty"`
	Definition struct {
		Links struct {
			Web struct {
				Href string `json:"href"`
			} `json:"web"`
			Self struct {
				Href string `json:"href"`
			} `json:"self"`
		} `json:"_links"`
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"definition"`
	Owner struct {
		Links struct {
			Web struct {
				Href string `json:"href"`
			} `json:"web"`
			Self struct {
				Href string `json:"href"`
			} `json:"self"`
		} `json:"_links"`
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"owner"`
	Data struct {
		ParallelismTag string `json:"ParallelismTag"`
		IsScheduledKey string `json:"IsScheduledKey"`
	} `json:"data"`
	PoolID          int    `json:"poolId"`
	OrchestrationID string `json:"orchestrationId"`
	Priority        int    `json:"priority"`
	MatchedAgents   *[]struct {
		Links struct {
			Self struct {
				Href string `json:"href"`
			} `json:"self"`
			Web struct {
				Href string `json:"href"`
			} `json:"web"`
		} `json:"_links"`
		ID                int    `json:"id"`
		Name              string `json:"name"`
		Version           string `json:"version"`
		Enabled           bool   `json:"enabled"`
		Status            string `json:"status"`
		ProvisioningState string `json:"provisioningState"`
	} `json:"matchedAgents,omitempty"`
}

// PendingJob is based on AzurePipelinesApiJobRequest, but has much fewer fields.
// The AzurePipelinesApiJobRequest.demands are turned into a string-string-map
type PendingJob struct {
	RequestID  int
	QueueTime  time.Time
	AssignTime time.Time
	Demands    map[string]string
}

type InexactMatchStringMap map[string]string

// IsInexactMatch returns true if all elements in `input` are also in `m`. `m` may contain one additional key named
// "ExtraAgentContainers"
func (m *InexactMatchStringMap) IsInexactMatch(input *map[string]string) bool {
	// Check that all elements in `input` are in m
	for k, v := range *input {
		if (*m)[k] != v {
			return false
		}
	}

	// Also check the other way round
	for k, v := range *m {
		if k == ExtraAgentContainersAnnotationKey {
			continue
		}
		if (*input)[k] != v {
			return false
		}
	}

	return true
}

// GetSortedStringificationOfMap returns a stringified map, of the form <key1>=<value1>,<key2>=<value2>,...
func (m *InexactMatchStringMap) GetSortedStringificationOfMap() string {
	return GetSortedStringificationOfCapabilitiesMap((*map[string]string)(m))
}

type PendingJobsWithDemands struct {
	demands     InexactMatchStringMap
	pendingJobs []PendingJob
}

type PendingJobsWrapper struct {
	PendingJobs []PendingJobsWithDemands
}

func (pjw *PendingJobsWrapper) GetInexactMatch(capabilities *map[string]string) *map[string][]PendingJob {
	result := map[string][]PendingJob{}

	for _, pendingJob := range pjw.PendingJobs {
		if pendingJob.demands.IsInexactMatch(capabilities) {
			result[pendingJob.demands.GetSortedStringificationOfMap()] = pendingJob.pendingJobs
		}
	}

	return &result
}

func (pjw *PendingJobsWrapper) AddJobRequest(jobRequestFromApi *AzurePipelinesApiJobRequest) {
	// Convert Azure Pipelines demand-strings (such as "myCustomCapability" or "Foo -equals bar") into a map:
	demandsAsMap := InexactMatchStringMap{}
	for _, demandString := range jobRequestFromApi.Demands {
		if strings.HasPrefix(demandString, "Agent.Version -gtVersion") {
			continue // It seems that ALL jobs have such kinds of demands, we ignore them
		}
		splitStr := strings.Split(demandString, " -equals ")
		if len(splitStr) == 1 {
			demandsAsMap[strings.TrimSpace(demandString)] = "1"
		} else if len(splitStr) == 2 {
			demandsAsMap[strings.TrimSpace(splitStr[0])] = strings.TrimSpace(splitStr[1])
		}
	}

	pendingJob := PendingJob{
		RequestID:  jobRequestFromApi.RequestID,
		QueueTime:  jobRequestFromApi.QueueTime,
		AssignTime: jobRequestFromApi.AssignTime,
		Demands:    demandsAsMap,
	}
	// Check whether there is already an entry for the demandsAsMap, if not, create one, otherwise
	// only append to the existing "PendingJobs" slice

	foundExistingEntry := false
	for i, pj := range pjw.PendingJobs {
		if reflect.DeepEqual(pj.demands, demandsAsMap) {
			(&pjw.PendingJobs[i]).pendingJobs = append((&pjw.PendingJobs[i]).pendingJobs, pendingJob)
			foundExistingEntry = true
			break
		}
	}

	if !foundExistingEntry {
		pjWithDemands := PendingJobsWithDemands{
			demands:     demandsAsMap,
			pendingJobs: []PendingJob{pendingJob},
		}
		pjw.PendingJobs = append(pjw.PendingJobs, pjWithDemands)
	}
}

type RunningPodsWithCapabilities struct {
	Capabilities InexactMatchStringMap
	RunningPods  []corev1.Pod
}

type RunningPodsWrapper struct {
	RunningPods []RunningPodsWithCapabilities
}

func NewRunningPodsWrapper(runningPods []corev1.Pod) *RunningPodsWrapper {
	rpw := RunningPodsWrapper{}

	for _, pod := range runningPods {
		// podCapabilitiesStr is something like: foo=bar;qux=1;hello=world
		podCapabilitiesStr := pod.Annotations[CapabilitiesAnnotationName]
		podCapabilitiesMap := (*InexactMatchStringMap)(GetCapabilitiesMapFromString(podCapabilitiesStr))

		foundExistingEntry := false
		for i, rp := range rpw.RunningPods {
			if reflect.DeepEqual(&rp.Capabilities, podCapabilitiesMap) {
				(&rpw.RunningPods[i]).RunningPods = append((&rpw.RunningPods[i]).RunningPods, pod)
				foundExistingEntry = true
				break
			}
		}

		if !foundExistingEntry {
			rpWithCapabilities := RunningPodsWithCapabilities{
				Capabilities: *podCapabilitiesMap,
				RunningPods:  []corev1.Pod{pod},
			}
			rpw.RunningPods = append(rpw.RunningPods, rpWithCapabilities)
		}
	}

	return &rpw
}

func (rpw *RunningPodsWrapper) GetInexactMatch(capabilities *map[string]string) *map[string][]corev1.Pod {
	result := map[string][]corev1.Pod{}

	for _, runningPod := range rpw.RunningPods {
		if runningPod.Capabilities.IsInexactMatch(capabilities) {
			result[runningPod.Capabilities.GetSortedStringificationOfMap()] = runningPod.RunningPods
		}
	}

	return &result
}

type PvcExhaustionError struct {
	ReusableCacheVolumeName string
}

func (e *PvcExhaustionError) Error() string {
	return e.ReusableCacheVolumeName
}
