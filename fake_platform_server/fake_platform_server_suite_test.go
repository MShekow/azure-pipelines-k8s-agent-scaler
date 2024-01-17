package fake_platform_server_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFakePlatformServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "FakePlatformServer Suite")
}
