/*
Copyright 2020 The cert-manager Authors.

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

package acme

import (
	"context"
	"encoding/base64"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cmacme "github.com/jetstack/cert-manager/pkg/apis/acme/v1"
	cmapi "github.com/jetstack/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/jetstack/cert-manager/pkg/apis/meta/v1"
	"github.com/jetstack/cert-manager/test/e2e/framework"
	"github.com/jetstack/cert-manager/test/e2e/framework/helper/featureset"
	"github.com/jetstack/cert-manager/test/e2e/suite/conformance/certificates"
)

var _ = framework.ConformanceDescribe("Certificates", func() {
	runACMEIssuerTests(nil)
})
var _ = framework.ConformanceDescribe("Certificates with External Account Binding", func() {
	runACMEIssuerTests(&cmacme.ACMEExternalAccountBinding{
		KeyID: "kid-1",
	})
})

func runACMEIssuerTests(eab *cmacme.ACMEExternalAccountBinding) {
	// unsupportedHTTP01Features is a list of features that are not supported by the ACME
	// issuer type using HTTP01
	var unsupportedHTTP01Features = featureset.NewFeatureSet(
		featureset.DurationFeature,
		featureset.WildcardsFeature,
		featureset.URISANsFeature,
		featureset.CommonNameFeature,
		featureset.KeyUsagesFeature,
		featureset.EmailSANsFeature,
		featureset.SaveCAToSecret,
		featureset.IssueCAFeature,
	)

	var unsupportedHTTP01GatewayFeatures = unsupportedHTTP01Features.Copy().Add(
		// Gateway API does not allow raw IP addresses to be specified
		// in HTTPRoutes, so challenges for an IP address will never work.
		featureset.IPAddressFeature,
	)

	// unsupportedDNS01Features is a list of features that are not supported by the ACME
	// issuer type using DNS01
	var unsupportedDNS01Features = featureset.NewFeatureSet(
		featureset.IPAddressFeature,
		featureset.DurationFeature,
		featureset.URISANsFeature,
		featureset.CommonNameFeature,
		featureset.KeyUsagesFeature,
		featureset.EmailSANsFeature,
		featureset.SaveCAToSecret,
		featureset.IssueCAFeature,
	)

	// UnsupportedPublicACMEServerFeatures are additional ACME features not supported by
	// public ACME servers
	var unsupportedPublicACMEServerFeatures = unsupportedHTTP01Features.Copy().Add(
		// Let's Encrypt doesn't yet support IP Address certificates.
		featureset.IPAddressFeature,
		// Ed25519 is not yet approved by the CA Browser forum.
		featureset.Ed25519FeatureSet,
		// Let's Encrypt copies one of the Subject alternative names to
		// the common name field. This field has a maximum total length of
		// 64 bytes. Skip the long domain test in this case.
		featureset.LongDomainFeatureSet,
	)

	provisionerHTTP01 := &acmeIssuerProvisioner{
		eab: eab,
	}

	provisionerDNS01 := &acmeIssuerProvisioner{
		eab: eab,
	}

	provisionerPACMEHTTP01 := &acmeIssuerProvisioner{
		eab: nil,
	}

	(&certificates.Suite{
		Name:                "ACME HTTP01 Issuer (Ingress)",
		HTTP01TestType:      "Ingress",
		CreateIssuerFunc:    provisionerHTTP01.createHTTP01IngressIssuer,
		DeleteIssuerFunc:    provisionerHTTP01.delete,
		UnsupportedFeatures: unsupportedHTTP01Features,
	}).Define()

	(&certificates.Suite{
		Name:                "ACME HTTP01 Issuer (Gateway)",
		HTTP01TestType:      "Gateway",
		CreateIssuerFunc:    provisionerHTTP01.createHTTP01GatewayIssuer,
		DeleteIssuerFunc:    provisionerHTTP01.delete,
		UnsupportedFeatures: unsupportedHTTP01GatewayFeatures,
	}).Define()

	(&certificates.Suite{
		Name:                "ACME DNS01 Issuer",
		DomainSuffix:        "dns01.example.com",
		CreateIssuerFunc:    provisionerDNS01.createDNS01Issuer,
		DeleteIssuerFunc:    provisionerDNS01.delete,
		UnsupportedFeatures: unsupportedDNS01Features,
	}).Define()

	(&certificates.Suite{
		Name:                "ACME HTTP01 ClusterIssuer (Ingress)",
		HTTP01TestType:      "Ingress",
		CreateIssuerFunc:    provisionerHTTP01.createHTTP01IngressClusterIssuer,
		DeleteIssuerFunc:    provisionerHTTP01.delete,
		UnsupportedFeatures: unsupportedHTTP01Features,
	}).Define()

	(&certificates.Suite{
		Name:                "ACME HTTP01 ClusterIssuer (Gateway)",
		HTTP01TestType:      "Gateway",
		CreateIssuerFunc:    provisionerHTTP01.createHTTP01GatewayClusterIssuer,
		DeleteIssuerFunc:    provisionerHTTP01.delete,
		UnsupportedFeatures: unsupportedHTTP01GatewayFeatures,
	}).Define()

	(&certificates.Suite{
		Name:                "ACME DNS01 ClusterIssuer",
		DomainSuffix:        "dns01.example.com",
		CreateIssuerFunc:    provisionerDNS01.createDNS01ClusterIssuer,
		DeleteIssuerFunc:    provisionerDNS01.delete,
		UnsupportedFeatures: unsupportedDNS01Features,
	}).Define()

	(&certificates.Suite{
		Name:                "Public ACME Server HTTP01 Issuer (Ingress)",
		HTTP01TestType:      "Ingress",
		CreateIssuerFunc:    provisionerPACMEHTTP01.createPublicACMEServerStagingHTTP01Issuer,
		DeleteIssuerFunc:    provisionerPACMEHTTP01.delete,
		UnsupportedFeatures: unsupportedPublicACMEServerFeatures,
	}).Define()
}

type acmeIssuerProvisioner struct {
	eab             *cmacme.ACMEExternalAccountBinding
	secretNamespace string
}

func (a *acmeIssuerProvisioner) delete(f *framework.Framework, ref cmmeta.ObjectReference) {
	if a.eab != nil {
		err := f.KubeClientSet.CoreV1().Secrets(a.secretNamespace).Delete(context.TODO(), a.eab.Key.Name, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())
	}

	if ref.Kind == "ClusterIssuer" {
		err := f.CertManagerClientSet.CertmanagerV1().ClusterIssuers().Delete(context.TODO(), ref.Name, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())
	}
}

// createXXX will deploy the required components to run an ACME issuer based test.
// This includes:
// - tiller
// - pebble
// - a properly configured Issuer resource

func (a *acmeIssuerProvisioner) createHTTP01IngressIssuer(f *framework.Framework) cmmeta.ObjectReference {
	a.ensureEABSecret(f, "")

	By("Creating an ACME HTTP01 Ingress Issuer")
	issuer := &cmapi.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "acme-issuer-http01-",
		},
		Spec: a.createHTTP01IngressIssuerSpec(f.Config.Addons.ACMEServer.URL),
	}

	issuer, err := f.CertManagerClientSet.CertmanagerV1().Issuers(f.Namespace.Name).Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create acme HTTP01 issuer")

	return cmmeta.ObjectReference{
		Group: cmapi.SchemeGroupVersion.Group,
		Kind:  cmapi.IssuerKind,
		Name:  issuer.Name,
	}
}

func (a *acmeIssuerProvisioner) createHTTP01IngressClusterIssuer(f *framework.Framework) cmmeta.ObjectReference {
	a.ensureEABSecret(f, f.Config.Addons.CertManager.ClusterResourceNamespace)

	By("Creating an ACME HTTP01 Ingress ClusterIssuer")
	issuer := &cmapi.ClusterIssuer{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "acme-cluster-issuer-http01-",
		},
		Spec: a.createHTTP01IngressIssuerSpec(f.Config.Addons.ACMEServer.URL),
	}

	issuer, err := f.CertManagerClientSet.CertmanagerV1().ClusterIssuers().Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create acme HTTP01 cluster issuer")

	return cmmeta.ObjectReference{
		Group: cmapi.SchemeGroupVersion.Group,
		Kind:  cmapi.ClusterIssuerKind,
		Name:  issuer.Name,
	}
}

func (a *acmeIssuerProvisioner) createHTTP01GatewayIssuer(f *framework.Framework) cmmeta.ObjectReference {
	a.ensureEABSecret(f, "")

	labelFlag := strings.Split(f.Config.Addons.Gateway.Labels, ",")
	labels := make(map[string]string)
	for _, l := range labelFlag {
		kv := strings.Split(l, "=")
		if len(kv) != 2 {
			continue
		}
		labels[kv[0]] = kv[1]
	}

	By("Creating an ACME HTTP01 Gateway Issuer")
	issuer := &cmapi.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "acme-issuer-http01-",
		},
		Spec: a.createHTTP01GatewayIssuerSpec(f.Config.Addons.ACMEServer.URL, labels),
	}

	issuer, err := f.CertManagerClientSet.CertmanagerV1().Issuers(f.Namespace.Name).Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create acme HTTP01 issuer")

	return cmmeta.ObjectReference{
		Group: cmapi.SchemeGroupVersion.Group,
		Kind:  cmapi.IssuerKind,
		Name:  issuer.Name,
	}
}

func (a *acmeIssuerProvisioner) createPublicACMEServerStagingHTTP01Issuer(f *framework.Framework) cmmeta.ObjectReference {
	By("Creating a Public ACME Server Staging HTTP01 Issuer")

	var PublicACMEServerStagingURL string
	if strings.Contains(f.Config.Addons.ACMEServer.URL, "pebble") {
		PublicACMEServerStagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
	} else {
		PublicACMEServerStagingURL = f.Config.Addons.ACMEServer.URL
	}

	issuer := &cmapi.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pacme-issuer-http01-",
		},
		Spec: a.createHTTP01IngressIssuerSpec(PublicACMEServerStagingURL),
	}

	issuer, err := f.CertManagerClientSet.CertmanagerV1().Issuers(f.Namespace.Name).Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create Public ACME Server Staging HTTP01 issuer")

	return cmmeta.ObjectReference{
		Group: cmapi.SchemeGroupVersion.Group,
		Kind:  cmapi.IssuerKind,
		Name:  issuer.Name,
	}
}

func (a *acmeIssuerProvisioner) createHTTP01GatewayClusterIssuer(f *framework.Framework) cmmeta.ObjectReference {
	a.ensureEABSecret(f, f.Config.Addons.CertManager.ClusterResourceNamespace)

	labelFlag := strings.Split(f.Config.Addons.Gateway.Labels, ",")
	labels := make(map[string]string)
	for _, l := range labelFlag {
		kv := strings.Split(l, "=")
		if len(kv) != 2 {
			continue
		}
		labels[kv[0]] = kv[1]
	}

	By("Creating an ACME HTTP01 Gateway ClusterIssuer")
	issuer := &cmapi.ClusterIssuer{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "acme-cluster-issuer-http01-",
		},
		Spec: a.createHTTP01GatewayIssuerSpec(f.Config.Addons.ACMEServer.URL, labels),
	}

	issuer, err := f.CertManagerClientSet.CertmanagerV1().ClusterIssuers().Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create acme HTTP01 cluster issuer")

	return cmmeta.ObjectReference{
		Group: cmapi.SchemeGroupVersion.Group,
		Kind:  cmapi.ClusterIssuerKind,
		Name:  issuer.Name,
	}
}

func (a *acmeIssuerProvisioner) createHTTP01IngressIssuerSpec(serverURL string) cmapi.IssuerSpec {
	return cmapi.IssuerSpec{
		IssuerConfig: cmapi.IssuerConfig{
			ACME: &cmacme.ACMEIssuer{
				Server:        serverURL,
				SkipTLSVerify: true,
				PrivateKey: cmmeta.SecretKeySelector{
					LocalObjectReference: cmmeta.LocalObjectReference{
						Name: "acme-private-key-http01",
					},
				},
				ExternalAccountBinding: a.eab,
				Solvers: []cmacme.ACMEChallengeSolver{
					{
						HTTP01: &cmacme.ACMEChallengeSolverHTTP01{
							// Not setting the Class or Name field will cause cert-manager to create
							// new ingress resources that do not specify a class to solve challenges,
							// which means all Ingress controllers should act on the ingresses.
							Ingress: &cmacme.ACMEChallengeSolverHTTP01Ingress{},
						},
					},
				},
			},
		},
	}
}

func (a *acmeIssuerProvisioner) createHTTP01GatewayIssuerSpec(serverURL string, labels map[string]string) cmapi.IssuerSpec {
	return cmapi.IssuerSpec{
		IssuerConfig: cmapi.IssuerConfig{
			ACME: &cmacme.ACMEIssuer{
				Server:        serverURL,
				SkipTLSVerify: true,
				PrivateKey: cmmeta.SecretKeySelector{
					LocalObjectReference: cmmeta.LocalObjectReference{
						Name: "acme-private-key-http01",
					},
				},
				ExternalAccountBinding: a.eab,
				Solvers: []cmacme.ACMEChallengeSolver{
					{
						HTTP01: &cmacme.ACMEChallengeSolverHTTP01{
							GatewayHTTPRoute: &cmacme.ACMEChallengeSolverHTTP01GatewayHTTPRoute{
								Labels: labels,
							},
						},
					},
				},
			},
		},
	}
}

func (a *acmeIssuerProvisioner) createDNS01Issuer(f *framework.Framework) cmmeta.ObjectReference {
	a.ensureEABSecret(f, f.Namespace.Name)

	By("Creating an ACME DNS01 Issuer")
	issuer := &cmapi.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "acme-issuer-dns01-",
		},
		Spec: a.createDNS01IssuerSpec(f.Config.Addons.ACMEServer.URL, f.Config.Addons.ACMEServer.DNSServer),
	}
	issuer, err := f.CertManagerClientSet.CertmanagerV1().Issuers(f.Namespace.Name).Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create acme DNS01 Issuer")

	return cmmeta.ObjectReference{
		Group: cmapi.SchemeGroupVersion.Group,
		Kind:  cmapi.IssuerKind,
		Name:  issuer.Name,
	}
}

func (a *acmeIssuerProvisioner) createDNS01ClusterIssuer(f *framework.Framework) cmmeta.ObjectReference {
	a.ensureEABSecret(f, f.Config.Addons.CertManager.ClusterResourceNamespace)

	By("Creating an ACME DNS01 ClusterIssuer")
	issuer := &cmapi.ClusterIssuer{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "acme-cluster-issuer-dns01-",
		},
		Spec: a.createDNS01IssuerSpec(f.Config.Addons.ACMEServer.URL, f.Config.Addons.ACMEServer.DNSServer),
	}
	issuer, err := f.CertManagerClientSet.CertmanagerV1().ClusterIssuers().Create(context.TODO(), issuer, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to create acme DNS01 ClusterIssuer")

	return cmmeta.ObjectReference{
		Group: cmapi.SchemeGroupVersion.Group,
		Kind:  cmapi.ClusterIssuerKind,
		Name:  issuer.Name,
	}
}

func (a *acmeIssuerProvisioner) createDNS01IssuerSpec(serverURL, dnsServer string) cmapi.IssuerSpec {
	return cmapi.IssuerSpec{
		IssuerConfig: cmapi.IssuerConfig{
			ACME: &cmacme.ACMEIssuer{
				Server:        serverURL,
				SkipTLSVerify: true,
				PrivateKey: cmmeta.SecretKeySelector{
					LocalObjectReference: cmmeta.LocalObjectReference{
						Name: "acme-private-key",
					},
				},
				ExternalAccountBinding: a.eab,
				Solvers: []cmacme.ACMEChallengeSolver{
					{
						DNS01: &cmacme.ACMEChallengeSolverDNS01{
							RFC2136: &cmacme.ACMEIssuerDNS01ProviderRFC2136{
								Nameserver: dnsServer,
							},
						},
					},
				},
			},
		},
	}
}

func (a *acmeIssuerProvisioner) ensureEABSecret(f *framework.Framework, ns string) {
	if a.eab == nil {
		return
	}

	if ns == "" {
		ns = f.Namespace.Name
	}
	sec, err := f.KubeClientSet.CoreV1().Secrets(ns).Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "external-account-binding-",
			Namespace:    ns,
		},
		Data: map[string][]byte{
			// base64 url encode (without padding) the HMAC key
			"key": []byte(base64.RawURLEncoding.EncodeToString([]byte("kid-secret-1"))),
		},
	}, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())

	a.eab.Key = cmmeta.SecretKeySelector{
		Key: "key",
		LocalObjectReference: cmmeta.LocalObjectReference{
			Name: sec.Name,
		},
	}

	a.secretNamespace = sec.Namespace
}
