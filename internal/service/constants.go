package service

const CapabilitiesAnnotationName = "AzurePipelinesCapabilities"
const ExtraAgentContainersAnnotationKey = "ExtraAgentContainers"
const ReusableCacheVolumeNameAnnotationKey = "ReusableCacheVolumeName"
const ReusableCacheVolumePromisedAnnotationKey = "PromisedForPod"
const PodTerminationInProgressAnnotationKey = "TerminationInProgress"
const AzureWorkingDirMountPath = "/azp/_work"
const AzureWorkingDirMountName = "workspace"
const DummyAgentNamePrefix = "dummy-agent"
const NonExistentContainerImageSuffix = "does-not-exist-for-sure"
