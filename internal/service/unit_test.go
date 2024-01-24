package service_test

/*
Contains various unit tests for the service package.
*/

import (
	apscalerv1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("ParseExtraAgentContainerDefinition unit tests", func() {
	It("empty / no containers", func() {
		containers, err := service.ParseExtraAgentContainerDefinition("")
		Expect(err).ToNot(HaveOccurred())
		Expect(containers).To(BeEmpty())
	})

	It("one container with requests and limits", func() {
		containers, err := service.ParseExtraAgentContainerDefinition("name=c,image=some-image:latest,cpu=500m,memory=2Gi")
		Expect(err).ToNot(HaveOccurred())
		Expect(containers).To(HaveLen(1))
		Expect(containers[0].Name).To(Equal("c"))
		Expect(containers[0].Image).To(Equal("some-image:latest"))

		expectedResources := corev1.ResourceRequirements{
			Limits: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
			},
			Requests: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
			},
		}

		Expect(containers[0].Resources).To(Equal(expectedResources))
	})

	It("one container with only memory requests and limits", func() {
		containers, err := service.ParseExtraAgentContainerDefinition("name=c,image=some-image:latest,memory=2Gi")
		Expect(err).ToNot(HaveOccurred())
		Expect(containers).To(HaveLen(1))
		Expect(containers[0].Name).To(Equal("c"))
		Expect(containers[0].Image).To(Equal("some-image:latest"))

		expectedResources := corev1.ResourceRequirements{
			Limits: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
			Requests: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
		}

		Expect(containers[0].Resources).To(Equal(expectedResources))
	})

	It("one container with only cpu requests and limits", func() {
		containers, err := service.ParseExtraAgentContainerDefinition("name=c,image=some-image:latest,cpu=500m")
		Expect(err).ToNot(HaveOccurred())
		Expect(containers).To(HaveLen(1))
		Expect(containers[0].Name).To(Equal("c"))
		Expect(containers[0].Image).To(Equal("some-image:latest"))

		expectedResources := corev1.ResourceRequirements{
			Limits: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU: resource.MustParse("500m"),
			},
			Requests: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU: resource.MustParse("500m"),
			},
		}

		Expect(containers[0].Resources).To(Equal(expectedResources))
	})

	It("one container without any requests and limits", func() {
		containers, err := service.ParseExtraAgentContainerDefinition("name=c,image=some-image:latest")
		Expect(err).ToNot(HaveOccurred())
		Expect(containers).To(HaveLen(1))
		Expect(containers[0].Name).To(Equal("c"))
		Expect(containers[0].Image).To(Equal("some-image:latest"))

		expectedResources := corev1.ResourceRequirements{}

		Expect(containers[0].Resources).To(Equal(expectedResources))
	})

	It("two containers with requests and limits", func() {
		containers, err := service.ParseExtraAgentContainerDefinition("name=c1,image=some-image:1,cpu=100m,memory=1Gi||name=c2,image=some-image:2,cpu=200m,memory=2Gi")
		Expect(err).ToNot(HaveOccurred())
		Expect(containers).To(HaveLen(2))
		Expect(containers[0].Name).To(Equal("c1"))
		Expect(containers[0].Image).To(Equal("some-image:1"))
		Expect(containers[1].Name).To(Equal("c2"))
		Expect(containers[1].Image).To(Equal("some-image:2"))

		expectedResources1 := corev1.ResourceRequirements{
			Limits: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
			Requests: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
		}
		expectedResources2 := corev1.ResourceRequirements{
			Limits: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
				corev1.ResourceCPU:    resource.MustParse("200m"),
			},
			Requests: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
				corev1.ResourceCPU:    resource.MustParse("200m"),
			},
		}

		Expect(containers[0].Resources).To(Equal(expectedResources1))
		Expect(containers[1].Resources).To(Equal(expectedResources2))
	})
})

var _ = Describe("ComputePvcMaxCountsPerReusableCacheVolume unit tests", func() {
	var agent *apscalerv1.AutoScaledAgent

	BeforeEach(func() {
		agent = &apscalerv1.AutoScaledAgent{
			Spec: apscalerv1.AutoScaledAgentSpec{
				ReusableCacheVolumes: []apscalerv1.ReusableCacheVolume{
					{
						Name:             "buildkit",
						StorageClassName: "hostpath",
						RequestedStorage: "10Gi",
					},
				},
			},
		}
	})

	It("one buildkit scenario used by a container of one pod template", func() {
		agent.Spec.PodsWithCapabilities = []apscalerv1.PodsWithCapabilities{
			{
				Capabilities: map[string]string{
					"buildkit": "1",
				},
				MinCount: &[]int32{0}[0],
				MaxCount: &[]int32{5}[0],
				PodTemplateSpec: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "buildkit",
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "buildkit",
										MountPath: "/buildkit",
									},
								},
							},
						},
					},
				},
			},
		}

		maxCounts := service.ComputePvcMaxCountsPerReusableCacheVolume(agent)
		Expect(*maxCounts).To(HaveLen(1))
		Expect((*maxCounts)["buildkit"]).To(Equal(5))

		// Verify that IsPvcLimitExceeded() returns false if we do not exceed the limit
		pvcs := []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "buildkit-0",
					Annotations: map[string]string{
						service.ReusableCacheVolumeNameAnnotationKey: "buildkit",
					},
				},
			},
		}
		Expect(service.IsPvcLimitExceeded(agent, "buildkit", pvcs)).To(BeFalse())

		// Verify that IsPvcLimitExceeded() returns true if we exceed the limit
		pvcs = []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "buildkit-0",
					Annotations: map[string]string{
						service.ReusableCacheVolumeNameAnnotationKey: "buildkit",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "buildkit-0",
					Annotations: map[string]string{
						service.ReusableCacheVolumeNameAnnotationKey: "buildkit",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "buildkit-0",
					Annotations: map[string]string{
						service.ReusableCacheVolumeNameAnnotationKey: "buildkit",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "buildkit-0",
					Annotations: map[string]string{
						service.ReusableCacheVolumeNameAnnotationKey: "buildkit",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "buildkit-0",
					Annotations: map[string]string{
						service.ReusableCacheVolumeNameAnnotationKey: "buildkit",
					},
				},
			},
		}
		Expect(service.IsPvcLimitExceeded(agent, "buildkit", pvcs)).To(BeTrue())
	})

	It("one buildkit cache volume used by a container in two different pod templates", func() {
		agent.Spec.PodsWithCapabilities = []apscalerv1.PodsWithCapabilities{
			{
				Capabilities: map[string]string{
					"buildkit": "1",
				},
				MinCount: &[]int32{0}[0],
				MaxCount: &[]int32{5}[0],
				PodTemplateSpec: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "buildkit",
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "buildkit",
										MountPath: "/buildkit",
									},
								},
							},
						},
					},
				},
			},
			{
				Capabilities: map[string]string{
					"buildkit": "1",
				},
				MinCount: &[]int32{0}[0],
				MaxCount: &[]int32{3}[0],
				PodTemplateSpec: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "buildkit",
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "buildkit",
										MountPath: "/buildkit",
									},
								},
							},
						},
					},
				},
			},
		}

		maxCounts := service.ComputePvcMaxCountsPerReusableCacheVolume(agent)
		Expect(*maxCounts).To(HaveLen(1))
		Expect((*maxCounts)["buildkit"]).To(Equal(8))
	})

	It("one buildkit cache volume that is used by no container", func() {
		agent.Spec.PodsWithCapabilities = []apscalerv1.PodsWithCapabilities{
			{
				Capabilities: map[string]string{
					"buildkit": "1",
				},
				MinCount: &[]int32{0}[0],
				MaxCount: &[]int32{5}[0],
				PodTemplateSpec: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "buildkit",
							},
						},
					},
				},
			},
		}

		maxCounts := service.ComputePvcMaxCountsPerReusableCacheVolume(agent)
		Expect(*maxCounts).To(BeEmpty())
	})
})

var _ = Describe("GetSortedStringificationOfCapabilitiesMap unit tests", func() {
	It("empty map", func() {
		capabilitiesMap := map[string]string{}
		Expect(service.GetSortedStringificationOfCapabilitiesMap(&capabilitiesMap)).To(Equal(""))

		// check the inverse
		Expect(service.GetCapabilitiesMapFromString("")).To(Equal(&capabilitiesMap))
	})

	It("one key-value pair", func() {
		capabilitiesMap := map[string]string{
			"key1": "value1",
		}
		expected := "key1=value1"
		Expect(service.GetSortedStringificationOfCapabilitiesMap(&capabilitiesMap)).To(Equal(expected))

		// check the inverse
		Expect(service.GetCapabilitiesMapFromString(expected)).To(Equal(&capabilitiesMap))
	})

	It("two key-value pairs", func() {
		capabilitiesMap := map[string]string{
			"key2": "value2",
			"key1": "value1",
		}
		expected := "key1=value1;key2=value2"
		Expect(service.GetSortedStringificationOfCapabilitiesMap(&capabilitiesMap)).To(Equal(expected))

		// check the inverse
		Expect(service.GetCapabilitiesMapFromString(expected)).To(Equal(&capabilitiesMap))
	})
})

var _ = Describe("InexactMatchStringMap unit tests", func() {
	It("empty map", func() {
		m := service.InexactMatchStringMap{}
		input := map[string]string{}
		Expect(m.IsInexactMatch(&input)).To(BeTrue())
	})

	It("one key-value pair", func() {
		m := service.InexactMatchStringMap{
			"key1": "value1",
		}
		input := map[string]string{
			"key1": "value1",
		}
		Expect(m.IsInexactMatch(&input)).To(BeTrue())
	})

	It("two key-value pairs with ExtraAgentContainersAnnotationKey", func() {
		m := service.InexactMatchStringMap{
			"key1": "value1",
			"key2": "value2",
			service.ExtraAgentContainersAnnotationKey: "foo",
		}
		input := map[string]string{
			"key1": "value1",
			"key2": "value2",
		}
		Expect(m.IsInexactMatch(&input)).To(BeTrue())
	})

	It("two key-value pairs mismatch", func() {
		m := service.InexactMatchStringMap{
			"key1": "value1",
			"key2": "value2",
			"key3": "value3",
		}
		input := map[string]string{
			"key1": "value1",
			"key2": "value2",
		}
		Expect(m.IsInexactMatch(&input)).To(BeFalse())
	})
})

var _ = Describe("GetSortedStringificationOfMap unit tests", func() {
	It("empty map", func() {
		m := service.InexactMatchStringMap{}
		Expect(m.GetSortedStringificationOfMap()).To(Equal(""))
	})

	It("one key-value pair", func() {
		m := service.InexactMatchStringMap{
			"key1": "value1",
		}
		expected := "key1=value1"
		Expect(m.GetSortedStringificationOfMap()).To(Equal(expected))
	})

	It("two key-value pairs", func() {
		m := service.InexactMatchStringMap{
			"key2": "value2",
			"key1": "value1",
		}
		expected := "key1=value1;key2=value2"
		Expect(m.GetSortedStringificationOfMap()).To(Equal(expected))
	})

})

var _ = Describe("NewRunningPodsWrapper unit tests", func() {
	It("empty list of pods", func() {
		var pods []corev1.Pod
		wrapper := service.NewRunningPodsWrapper(pods)
		Expect(wrapper.RunningPods).To(BeEmpty())
	})

	It("one pod with no capabilities", func() {
		pods := []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "pod1",
					Annotations: map[string]string{},
				},
			},
		}
		wrapper := service.NewRunningPodsWrapper(pods)
		Expect(wrapper.RunningPods).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].RunningPods).To(Equal(pods))
		Expect(wrapper.RunningPods[0].Capabilities).To(BeEmpty())
	})

	It("one pod with one capability", func() {
		pods := []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "foo=bar",
					},
				},
			},
		}
		wrapper := service.NewRunningPodsWrapper(pods)
		Expect(wrapper.RunningPods).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].RunningPods).To(Equal(pods))
		Expect(wrapper.RunningPods[0].Capabilities).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].Capabilities["foo"]).To(Equal("bar"))
	})

	It("two pods with the same capabilities being grouped together", func() {
		pods := []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "foo=bar",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod2",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "foo=bar",
					},
				},
			},
		}
		wrapper := service.NewRunningPodsWrapper(pods)
		Expect(wrapper.RunningPods).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].RunningPods).To(Equal(pods))
		Expect(wrapper.RunningPods[0].Capabilities).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].Capabilities["foo"]).To(Equal("bar"))
	})

	It("two pods with different capabilities (ExtraAgentContainers not involved)", func() {
		pods := []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "foo=bar",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod2",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "qux=1",
					},
				},
			},
		}
		wrapper := service.NewRunningPodsWrapper(pods)
		Expect(wrapper.RunningPods).To(HaveLen(2))
		Expect(wrapper.RunningPods[0].RunningPods).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].RunningPods[0]).To(Equal(pods[0]))
		Expect(wrapper.RunningPods[0].Capabilities).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].Capabilities["foo"]).To(Equal("bar"))
		Expect(wrapper.RunningPods[1].RunningPods).To(HaveLen(1))
		Expect(wrapper.RunningPods[1].RunningPods[0]).To(Equal(pods[1]))
		Expect(wrapper.RunningPods[1].Capabilities).To(HaveLen(1))
		Expect(wrapper.RunningPods[1].Capabilities["qux"]).To(Equal("1"))
	})

	It("two pods with different capabilities (involving ExtraAgentContainers) are grouped", func() {
		pods := []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName:        "foo=bar",
						service.ExtraAgentContainersAnnotationKey: "name=c,image=some-image:latest,cpu=500m,memory=2Gi",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod2",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "foo=bar",
					},
				},
			},
		}
		wrapper := service.NewRunningPodsWrapper(pods)
		Expect(wrapper.RunningPods).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].RunningPods).To(Equal(pods))
		Expect(wrapper.RunningPods[0].Capabilities).To(HaveLen(1))
		Expect(wrapper.RunningPods[0].Capabilities["foo"]).To(Equal("bar"))
	})

	It("GetInexactMatch() returns the correct pods", func() {
		pods := []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "foo=bar",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod2",
					Annotations: map[string]string{
						service.CapabilitiesAnnotationName: "foo2=bar2",
					},
				},
			},
		}
		wrapper := service.NewRunningPodsWrapper(pods)
		Expect(wrapper.RunningPods).To(HaveLen(2))

		foo := map[string]string{
			"foo": "bar",
		}

		fooResult := wrapper.GetInexactMatch(&foo)
		Expect(*fooResult).To(HaveLen(1))
		Expect((*fooResult)["foo=bar"]).To(Equal(pods[0:1]))

		foo2 := map[string]string{
			"foo2": "bar2",
		}

		foo2Result := wrapper.GetInexactMatch(&foo2)
		Expect(*foo2Result).To(HaveLen(1))
		Expect((*foo2Result)["foo2=bar2"]).To(Equal(pods[1:2]))

		fooEmptyResult := wrapper.GetInexactMatch(&map[string]string{})
		Expect(*fooEmptyResult).To(HaveLen(0))
	})
})
