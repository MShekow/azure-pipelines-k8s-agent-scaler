package service

import "time"

const CapabilitiesAnnotationName = "AzurePipelinesCapabilities"
const ExtraAgentContainersAnnotationKey = "ExtraAgentContainers"
const ReusableCacheVolumeNameAnnotationKey = "ReusableCacheVolumeName"
const ReusableCacheVolumePromisedAnnotationKey = "PromisedForPod"
const PodTerminationInProgressAnnotationKey = "TerminationInProgress"
const IdleAgentPodFirstDetectionTimestampAnnotationKey = "FirstIdleDetectionTimestamp"
const AzureWorkingDirMountPath = "/azp/_work"
const AzureWorkingDirMountName = "workspace"
const DummyAgentNamePrefix = "dummy-agent"
const NonExistentContainerImageSuffix = "does-not-exist-for-sure"
const DebugLogEnvVarName = "DEBUG_FILE_PATH"
const AgentMinIdlePeriodDefault = time.Duration(5 * time.Minute)
