package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

var testProviders = map[string]func() (tfprotov6.ProviderServer, error){
	"sonarqube": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck skips unless TF_ACC and a server are configured
// (SONAR_HOST plus SONAR_TOKEN or SONAR_USER/SONAR_PASS).
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("SONAR_HOST") == "" {
		t.Fatal("SONAR_HOST must be set for acceptance tests")
	}
}

// testClient returns a client for out-of-band changes in tests. It skips
// the test without TF_ACC, since callers reach the server before
// resource.Test would skip on its own.
func testClient(t *testing.T) *sonarqube.Client {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests skipped unless env 'TF_ACC' set")
	}
	c, err := sonarqube.NewClient(os.Getenv("SONAR_HOST"), sonarqube.WithToken(os.Getenv("SONAR_TOKEN")))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testEdition(t *testing.T) string {
	t.Helper()
	nav, _, err := testClient(t).Navigation.Global(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(nav.Edition)
}

// requireEdition skips the test unless the test server is at least minEdition.
func requireEdition(t *testing.T, minEdition string) {
	t.Helper()
	if ed := testEdition(t); editionRank[ed] < editionRank[minEdition] {
		t.Skipf("needs %s edition, server is %s", minEdition, ed)
	}
}

func rname(prefix string) string {
	return fmt.Sprintf("tf-acc-%s-%s", prefix, acctest.RandString(8))
}
