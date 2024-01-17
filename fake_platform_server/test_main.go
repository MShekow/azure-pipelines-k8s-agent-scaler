package fake_platform_server

import "time"

func MainTest() {
	// Create the fake platform server
	fakePlatformServer := NewFakeAzurePipelinesPlatformServer()

	fakePlatformServer.CreatePool(1, "Default")

	// Start the fake platform server
	err := fakePlatformServer.Start(0)
	if err != nil {
		panic(err)
	}

	fakeDemands := map[string]string{
		"foo": "bar",
	}
	err = fakePlatformServer.AddJob(2, 1, int64(30*time.Second), fakeDemands)
	if err != nil {
		panic(err)
	}

	time.Sleep(335 * time.Second)

	err = fakePlatformServer.Stop()
	if err != nil {
		panic(err)
	}
}
