/*
Copyright 2021 The cert-manager Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package venafi

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jetstack/cert-manager/pkg/controller/certificatesigningrequests/util"
	"github.com/jetstack/cert-manager/test/e2e/framework"
	"github.com/jetstack/cert-manager/test/e2e/framework/addon/venafi"
	"github.com/jetstack/cert-manager/test/e2e/framework/helper/featureset"
	"github.com/jetstack/cert-manager/test/e2e/framework/util/errors"
	"github.com/jetstack/cert-manager/test/e2e/suite/conformance/certificatesigningrequests"
)

var _ = framework.ConformanceDescribe("CertificateSigningRequests", func() {
	// unsupportedFeatures is a list of features that are not supported by the
	// Venafi TPP issuer.
	var unsupportedFeatures = featureset.NewFeatureSet(
		// Venafi TPP doesn't allow setting a duration
		featureset.DurationFeature,
		// Due to the current configuration of the test environment, it does not
		// support signing certificates that pair with an elliptic curve or
		// Ed255119 private keys
		featureset.ECDSAFeature,
		featureset.Ed25519FeatureSet,
		// Our Venafi TPP doesn't allow setting non DNS SANs
		// TODO: investigate options to enable these
		featureset.EmailSANsFeature,
		featureset.URISANsFeature,
		featureset.IPAddressFeature,
		// Venafi doesn't allow certs with empty CN & DN
		featureset.OnlySAN,
		// Venafi doesn't setting key usages.
		featureset.KeyUsagesFeature,
	)

	venafiIssuer := new(cloud)
	(&certificatesigningrequests.Suite{
		Name:                "Venafi Cloud Issuer",
		CreateIssuerFunc:    venafiIssuer.createIssuer,
		DeleteIssuerFunc:    venafiIssuer.delete,
		UnsupportedFeatures: unsupportedFeatures,
	}).Define()

	venafiClusterIssuer := new(cloud)
	(&certificatesigningrequests.Suite{
		Name:                "Venafi Cloud Cluster Issuer",
		CreateIssuerFunc:    venafiClusterIssuer.createClusterIssuer,
		DeleteIssuerFunc:    venafiClusterIssuer.delete,
		UnsupportedFeatures: unsupportedFeatures,
	}).Define()
})

type cloud struct {
	*venafi.VenafiCloud
}

func (c *cloud) delete(f *framework.Framework, signerName string) {
	Expect(c.Deprovision()).NotTo(HaveOccurred(), "failed to deprovision cloud venafi")

	ref, _ := util.SignerIssuerRefFromSignerName(signerName)
	if ref.Type == "clusterissuers" {
		err := f.CertManagerClientSet.CertmanagerV1().ClusterIssuers().Delete(context.TODO(), ref.Name, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())
	}
}

func (c *cloud) createIssuer(f *framework.Framework) string {
	By("Creating a Venafi Cloud Issuer")

	c.VenafiCloud = &venafi.VenafiCloud{
		Namespace: f.Namespace.Name,
	}

	err := c.Setup(f.Config)
	if errors.IsSkip(err) {
		framework.Skipf("Skipping test as addon could not be setup: %v", err)
	}
	Expect(err).NotTo(HaveOccurred(), "failed to provision venafi cloud issuer")

	Expect(c.Provision()).NotTo(HaveOccurred(), "failed to provision tpp venafi")

	issuer := c.Details().BuildIssuer()
	issuer, err = f.CertManagerClientSet.CertmanagerV1().Issuers(f.Namespace.Name).Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create issuer for venafi")

	return fmt.Sprintf("issuers.cert-manager.io/%s.%s", issuer.Namespace, issuer.Name)
}

// createClusterIssuer creates and returns name of a Venafi Cloud
// ClusterIssuer. The name is of the form
// "clusterissuers.cert-manager.io/issuer-ab3de1".
func (c *cloud) createClusterIssuer(f *framework.Framework) string {
	By("Creating a Venafi Cloud ClusterIssuer")

	c.VenafiCloud = &venafi.VenafiCloud{
		Namespace: f.Config.Addons.CertManager.ClusterResourceNamespace,
	}

	err := c.Setup(f.Config)
	if errors.IsSkip(err) {
		framework.Skipf("Skipping test as addon could not be setup: %v", err)
	}
	Expect(err).NotTo(HaveOccurred(), "failed to setup tpp venafi")

	Expect(c.Provision()).NotTo(HaveOccurred(), "failed to provision tpp venafi")

	issuer := c.Details().BuildClusterIssuer()
	issuer, err = f.CertManagerClientSet.CertmanagerV1().ClusterIssuers().Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create issuer for venafi")

	return fmt.Sprintf("clusterissuers.cert-manager.io/%s", issuer.Name)
}
