// Copyright (c) 2017-2025 Tigera, Inc. All rights reserved.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clientv3_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	apiv3 "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectcalico/calico/libcalico-go/lib/apiconfig"
	"github.com/projectcalico/calico/libcalico-go/lib/backend"
	bapi "github.com/projectcalico/calico/libcalico-go/lib/backend/api"
	"github.com/projectcalico/calico/libcalico-go/lib/backend/k8s"
	"github.com/projectcalico/calico/libcalico-go/lib/clientv3"
	"github.com/projectcalico/calico/libcalico-go/lib/names"
	"github.com/projectcalico/calico/libcalico-go/lib/options"
	"github.com/projectcalico/calico/libcalico-go/lib/testutils"
	"github.com/projectcalico/calico/libcalico-go/lib/watch"
)

func tieredGNPName(p, t string) string {
	name, _ := names.BackendTieredPolicyName(p, t)
	return name
}

var (
	ingressEgress = []apiv3.PolicyType{apiv3.PolicyTypeIngress, apiv3.PolicyTypeEgress}
	ingress       = []apiv3.PolicyType{apiv3.PolicyTypeIngress}
	egress        = []apiv3.PolicyType{apiv3.PolicyTypeEgress}
)

var _ = testutils.E2eDatastoreDescribe("GlobalNetworkPolicy tests", testutils.DatastoreAll, func(config apiconfig.CalicoAPIConfig) {
	ctx := context.Background()
	order1 := 99.999
	order2 := 22.222
	tier := "tier-a"
	tierOrder := float64(10)
	actionDeny := apiv3.Deny
	name1 := "globalnetworkp-1"
	name2 := "globalnetworkp-2"
	spec1 := apiv3.GlobalNetworkPolicySpec{
		Order:    &order1,
		Ingress:  []apiv3.Rule{testutils.InRule1, testutils.InRule2},
		Egress:   []apiv3.Rule{testutils.EgressRule1, testutils.EgressRule2},
		Selector: "thing == 'value'",
	}
	spec2 := apiv3.GlobalNetworkPolicySpec{
		Order:          &order2,
		Ingress:        []apiv3.Rule{testutils.InRule2, testutils.InRule1},
		Egress:         []apiv3.Rule{testutils.EgressRule2, testutils.EgressRule1},
		Selector:       "thing2 == 'value2'",
		DoNotTrack:     true,
		ApplyOnForward: true,
	}
	// Specs with only ingress or egress rules, without Types set.
	ingressSpec1 := spec1
	ingressSpec1.Egress = nil
	egressSpec2 := spec2
	egressSpec2.Ingress = nil
	// Specs with ingress and egress rules, with Types set to just ingress or egress.
	ingressTypesSpec1 := spec1
	ingressTypesSpec1.Types = ingress
	egressTypesSpec2 := spec2
	egressTypesSpec2.Types = egress

	var c clientv3.Interface
	var be bapi.Client

	BeforeEach(func() {
		var err error
		c, err = clientv3.New(config)
		Expect(err).NotTo(HaveOccurred())

		be, err = backend.NewClient(config)
		Expect(err).NotTo(HaveOccurred())
		be.Clean()

		err = c.EnsureInitialized(ctx, "", "")
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("GlobalNetworkPolicy e2e CRUD tests",
		func(tier, name1, name2 string, spec1, spec2 apiv3.GlobalNetworkPolicySpec, types1, types2 []apiv3.PolicyType) {
			spec1.Tier = tier
			spec2.Tier = tier

			if tier != "" && tier != "default" {
				// Create the tier if required before running other tiered policy tests.
				tierSpec := apiv3.TierSpec{Order: &tierOrder, DefaultAction: &actionDeny}
				By("Creating the tier")
				tierRes, resErr := c.Tiers().Create(ctx, &apiv3.Tier{
					ObjectMeta: metav1.ObjectMeta{Name: tier},
					Spec:       tierSpec,
				}, options.SetOptions{})
				Expect(resErr).NotTo(HaveOccurred())
				Expect(tierRes).To(MatchResource(apiv3.KindTier, testutils.ExpectNoNamespace, tier, tierSpec))
			}

			By("Updating the GlobalNetworkPolicy before it is created")
			_, outError := c.GlobalNetworkPolicies().Update(ctx, &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "1234", CreationTimestamp: metav1.Now(), UID: uid},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + tieredGNPName(name1, tier) + ") with error:"))

			By("Attempting to creating a new GlobalNetworkPolicy with name1/spec1 and a non-empty ResourceVersion")
			polToCreate := &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "12345"},
				Spec:       spec1,
			}
			polToCreateCopy := polToCreate.DeepCopy()
			_, outError = c.GlobalNetworkPolicies().Create(ctx, polToCreate, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("error with field Metadata.ResourceVersion = '12345' (field must not be set for a Create request)"))
			Expect(polToCreate).To(Equal(polToCreateCopy), "Create() unexpectedly modified input policy")

			By("Creating a new GlobalNetworkPolicy with name1/spec1")
			polToCreate = &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name1},
				Spec:       spec1,
			}
			polToCreateCopy = polToCreate.DeepCopy()
			res1, outError := c.GlobalNetworkPolicies().Create(ctx, polToCreate, options.SetOptions{})
			Expect(polToCreate).To(Equal(polToCreateCopy), "Create() unexpectedly modified input policy")
			Expect(outError).NotTo(HaveOccurred())
			spec1.Types = types1
			Expect(res1).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec1))

			// Track the version of the original data for name1.
			rv1_1 := res1.ResourceVersion

			By("Attempting to create the same GlobalNetworkPolicy with name1 but with spec2")
			_, outError = c.GlobalNetworkPolicies().Create(ctx, &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name1},
				Spec:       spec2,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("resource already exists: GlobalNetworkPolicy(" + tieredGNPName(name1, tier) + ")"))

			By("Getting GlobalNetworkPolicy (name1) and comparing the output against spec1")
			res, outError := c.GlobalNetworkPolicies().Get(ctx, tieredGNPName(name1, tier), options.GetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec1))
			Expect(res.ResourceVersion).To(Equal(res1.ResourceVersion))

			By("Getting GlobalNetworkPolicy (name2) before it is created")
			_, outError = c.GlobalNetworkPolicies().Get(ctx, name2, options.GetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + tieredGNPName(name2, tier) + ") with error:"))

			By("Listing all the GlobalNetworkPolicies, expecting a single result with name1/spec1")
			outList, outError := c.GlobalNetworkPolicies().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(ConsistOf(
				testutils.Resource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec1),
			))

			By("Creating a new GlobalNetworkPolicy with name2/spec2")
			res2, outError := c.GlobalNetworkPolicies().Create(ctx, &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name2},
				Spec:       spec2,
			}, options.SetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			spec2.Types = types2
			Expect(res2).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name2, tier), spec2))

			By("Getting GlobalNetworkPolicy (name2) and comparing the output against spec2")
			res, outError = c.GlobalNetworkPolicies().Get(ctx, name2, options.GetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res2).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name2, tier), spec2))
			Expect(res.ResourceVersion).To(Equal(res2.ResourceVersion))

			By("Listing all the GlobalNetworkPolicies, expecting a two results with name1/spec1 and name2/spec2")
			outList, outError = c.GlobalNetworkPolicies().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(ConsistOf(
				testutils.Resource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec1),
				testutils.Resource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name2, tier), spec2),
			))

			By("Updating GlobalNetworkPolicy name1 with spec2")
			res1.Spec = spec2
			res1Copy := res1.DeepCopy()
			res1out, outError := c.GlobalNetworkPolicies().Update(ctx, res1, options.SetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res1.Spec).To(Equal(res1Copy.Spec), "Update() unexpectedly modified input")
			Expect(res1).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec2))
			res1 = res1out

			By("Attempting to update the GlobalNetworkPolicy without a Creation Timestamp")
			res, outError = c.GlobalNetworkPolicies().Update(ctx, &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "1234", UID: uid},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(res).To(BeNil())
			Expect(outError.Error()).To(Equal("error with field Metadata.CreationTimestamp = '0001-01-01 00:00:00 +0000 UTC' (field must be set for an Update request)"))

			By("Attempting to update the GlobalNetworkPolicy without a UID")
			res, outError = c.GlobalNetworkPolicies().Update(ctx, &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "1234", CreationTimestamp: metav1.Now()},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(res).To(BeNil())
			Expect(outError.Error()).To(Equal("error with field Metadata.UID = '' (field must be set for an Update request)"))

			// Track the version of the updated name1 data.
			rv1_2 := res1.ResourceVersion

			By("Updating GlobalNetworkPolicy name1 without specifying a resource version")
			res1.Spec = spec1
			res1.ObjectMeta.ResourceVersion = ""
			_, outError = c.GlobalNetworkPolicies().Update(ctx, res1, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("error with field Metadata.ResourceVersion = '' (field must be set for an Update request)"))

			By("Updating GlobalNetworkPolicy name1 using the previous resource version")
			res1.Spec = spec1
			res1.ResourceVersion = rv1_1
			_, outError = c.GlobalNetworkPolicies().Update(ctx, res1, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("update conflict: GlobalNetworkPolicy(" + tieredGNPName(name1, tier) + ")"))

			if config.Spec.DatastoreType != apiconfig.Kubernetes {
				By("Getting GlobalNetworkPolicy (name1) with the original resource version and comparing the output against spec1")
				res, outError = c.GlobalNetworkPolicies().Get(ctx, name1, options.GetOptions{ResourceVersion: rv1_1})
				Expect(outError).NotTo(HaveOccurred())
				Expect(res).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec1))
				Expect(res.ResourceVersion).To(Equal(rv1_1))
			}

			By("Getting GlobalNetworkPolicy (name1) with the updated resource version and comparing the output against spec2")
			res, outError = c.GlobalNetworkPolicies().Get(ctx, name1, options.GetOptions{ResourceVersion: rv1_2})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec2))
			Expect(res.ResourceVersion).To(Equal(rv1_2))

			if config.Spec.DatastoreType != apiconfig.Kubernetes {
				By("Listing GlobalNetworkPolicies with the original resource version and checking for a single result with name1/spec1")
				outList, outError = c.GlobalNetworkPolicies().List(ctx, options.ListOptions{ResourceVersion: rv1_1})
				Expect(outError).NotTo(HaveOccurred())
				Expect(outList.Items).To(ConsistOf(
					testutils.Resource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec1),
				))
			}

			By("Listing GlobalNetworkPolicies with the latest resource version and checking for two results with name1/spec2 and name2/spec2")
			outList, outError = c.GlobalNetworkPolicies().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(ConsistOf(
				testutils.Resource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec2),
				testutils.Resource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name2, tier), spec2),
			))

			if config.Spec.DatastoreType != apiconfig.Kubernetes {
				By("Deleting GlobalNetworkPolicy (name1) with the old resource version")
				_, outError = c.GlobalNetworkPolicies().Delete(ctx, name1, options.DeleteOptions{ResourceVersion: rv1_1})
				Expect(outError).To(HaveOccurred())
				Expect(outError.Error()).To(Equal("update conflict: GlobalNetworkPolicy(" + tieredGNPName(name1, tier) + ")"))
			}

			By("Deleting GlobalNetworkPolicy (name1) with the new resource version")
			dres, outError := c.GlobalNetworkPolicies().Delete(ctx, name1, options.DeleteOptions{ResourceVersion: rv1_2})
			Expect(outError).NotTo(HaveOccurred())
			Expect(dres).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name1, tier), spec2))
			time.Sleep(1 * time.Second)

			if config.Spec.DatastoreType != apiconfig.Kubernetes {
				By("Updating GlobalNetworkPolicy name2 with a 2s TTL and waiting for the entry to be deleted")
				_, outError = c.GlobalNetworkPolicies().Update(ctx, res2, options.SetOptions{TTL: 2 * time.Second})
				Expect(outError).NotTo(HaveOccurred())
				time.Sleep(1 * time.Second)
				_, outError = c.GlobalNetworkPolicies().Get(ctx, name2, options.GetOptions{})
				Expect(outError).NotTo(HaveOccurred())
				time.Sleep(2 * time.Second)
				_, outError = c.GlobalNetworkPolicies().Get(ctx, name2, options.GetOptions{})
				Expect(outError).To(HaveOccurred())
				Expect(outError.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + tieredGNPName(name2, tier) + ") with error:"))

				By("Creating GlobalNetworkPolicy name2 with a 2s TTL and waiting for the entry to be deleted")
				_, outError = c.GlobalNetworkPolicies().Create(ctx, &apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: name2},
					Spec:       spec2,
				}, options.SetOptions{TTL: 2 * time.Second})
				Expect(outError).NotTo(HaveOccurred())
				time.Sleep(1 * time.Second)
				_, outError = c.GlobalNetworkPolicies().Get(ctx, name2, options.GetOptions{})
				Expect(outError).NotTo(HaveOccurred())
				time.Sleep(2 * time.Second)
				_, outError = c.GlobalNetworkPolicies().Get(ctx, name2, options.GetOptions{})
				Expect(outError).To(HaveOccurred())
				Expect(outError.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + tieredGNPName(name2, tier) + ") with error:"))
			}

			if config.Spec.DatastoreType == apiconfig.Kubernetes {
				By("Deleting GlobalNetworkPolicy (name2) for KDD that does not support TTL")
				dres, outError = c.GlobalNetworkPolicies().Delete(ctx, name2, options.DeleteOptions{})
				Expect(outError).NotTo(HaveOccurred())
				Expect(dres).To(MatchResource(apiv3.KindGlobalNetworkPolicy, testutils.ExpectNoNamespace, tieredGNPName(name2, tier), spec2))
				time.Sleep(1 * time.Second)
			}

			By("Attempting to delete GlobalNetworkPolicy (name2) again")
			_, outError = c.GlobalNetworkPolicies().Delete(ctx, name2, options.DeleteOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + tieredGNPName(name2, tier) + ") with error:"))

			By("Listing all GlobalNetworkPolicies and expecting no items")
			outList, outError = c.GlobalNetworkPolicies().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(HaveLen(0))

			By("Getting GlobalNetworkPolicy (name2) and expecting an error")
			_, outError = c.GlobalNetworkPolicies().Get(ctx, name2, options.GetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + tieredGNPName(name2, tier) + ") with error:"))
		},

		// Pass two fully populated GlobalNetworkPolicySpecs in default tier and expect the series of operations to succeed.
		Entry("Two fully populated GlobalNetworkPolicySpecs", "default", "default."+name1, "default."+name2, spec1, spec2, ingressEgress, ingressEgress),
		// Check defaulting for policies with ingress rules and egress rules only.
		Entry("Ingress-only and egress-only policies", "default", "default."+name1, "default."+name2, ingressSpec1, egressSpec2, ingress, egress),
		// Check non-defaulting for policies with explicit Types value.
		Entry("Policies with explicit ingress and egress Types", "default", "default."+name1, "default."+name2, ingressTypesSpec1, egressTypesSpec2, ingress, egress),
		// Pass two fully populated GlobalNetworkPolicySpecs in a tier, and expect the series of operations to succeed.
		Entry("Two fully populated GlobalNetworkPolicySpecs", tier, tier+"."+name1, tier+"."+name2, spec1, spec2, ingressEgress, ingressEgress),
	)

	DescribeTable("GlobalNetworkPolicy default tier name test",
		func(policyName string, incorrectPrefixPolicyName string) {
			By("Getting the policy before it was created")
			_, err := c.GlobalNetworkPolicies().Get(ctx, policyName, options.GetOptions{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + policyName + ") with error:"))

			By("Updating the policy before it was created")
			_, err = c.GlobalNetworkPolicies().Update(ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: policyName, ResourceVersion: "1234", CreationTimestamp: metav1.Now(), UID: uid},
				}, options.SetOptions{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("resource does not exist: GlobalNetworkPolicy(" + policyName + ") with error:"))

			By("Creating the policy")
			returnedPolicy, err := c.GlobalNetworkPolicies().Create(ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: policyName},
				}, options.SetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(returnedPolicy.Name).To(Equal(policyName))

			By("Creating the policy with incorrect prefix name")
			_, err = c.GlobalNetworkPolicies().Create(ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: incorrectPrefixPolicyName},
				}, options.SetOptions{})
			Expect(err).To(HaveOccurred())

			By("Getting the policy")
			returnedPolicy, err = c.GlobalNetworkPolicies().Get(ctx, policyName, options.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(returnedPolicy.Name).To(Equal(policyName))

			By("Updating the policy")
			returnedPolicy, err = c.GlobalNetworkPolicies().Update(ctx, returnedPolicy, options.SetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(returnedPolicy.Name).To(Equal(policyName))

			By("Getting the policy with incorrect prefix")
			_, err = c.GlobalNetworkPolicies().Get(ctx, incorrectPrefixPolicyName, options.GetOptions{})
			Expect(err).To(HaveOccurred())

			By("Updating the policy with incorrect prefix")
			_, err = c.GlobalNetworkPolicies().Update(ctx, &apiv3.GlobalNetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: incorrectPrefixPolicyName, ResourceVersion: "1234", CreationTimestamp: metav1.Now(), UID: uid},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(err).To(HaveOccurred())

			By("Deleting policy")
			returnedPolicy, err = c.GlobalNetworkPolicies().Delete(ctx, policyName, options.DeleteOptions{})
			Expect(returnedPolicy.Name).To(Equal(policyName))
			Expect(err).ToNot(HaveOccurred())
		},
		Entry("GlobalNetworkPolicy without default tier prefix", "netpol", "default.netpol"),
		Entry("GlobalNetworkPolicy with default tier prefix", "default.netpol", "netpol"),
	)

	Describe("GlobalNetworkPolicy without name on the projectcalico.org annotation", func() {
		It("Should return the name without default prefix", func() {
			if config.Spec.DatastoreType == apiconfig.Kubernetes {
				config, _, err := k8s.CreateKubernetesClientset(&config.Spec)
				Expect(err).NotTo(HaveOccurred())
				config.ContentType = "application/json"
				cli, err := ctrlclient.New(config, ctrlclient.Options{})
				Expect(err).NotTo(HaveOccurred())

				// Create v1 crd with empty metadata annotation name
				annotations := map[string]string{}
				annotations["projectcalico.org/metadata"] = "{}"
				policy := &apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: annotations,
						Name:        "default.prefix-test-policy"},
					Spec: apiv3.GlobalNetworkPolicySpec{},
				}
				err = cli.Create(context.Background(), policy)
				Expect(err).NotTo(HaveOccurred())

				// We should be able to get it without the default. prefix
				_, err = c.GlobalNetworkPolicies().Get(ctx, "prefix-test-policy", options.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
			}
		})
	})

	DescribeTable("GlobalNetworkPolicy name validation tests",
		func(policyName string, tier string, expectError bool) {
			if tier != "default" {
				// Create the tier if required before running other tiered policy tests.
				tierSpec := apiv3.TierSpec{Order: &tierOrder}
				By("Creating the tier")
				_, resErr := c.Tiers().Create(ctx, &apiv3.Tier{
					ObjectMeta: metav1.ObjectMeta{Name: tier},
					Spec:       tierSpec,
				}, options.SetOptions{})
				Expect(resErr).NotTo(HaveOccurred())
			}

			_, err := c.GlobalNetworkPolicies().Create(ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name: policyName},
					Spec: apiv3.GlobalNetworkPolicySpec{
						Tier: tier,
					},
				}, options.SetOptions{})

			if expectError {
				Expect(err).To(HaveOccurred())
			} else {
				Expect(err).ToNot(HaveOccurred())
			}
		},
		Entry("GlobalNetworkPolicy in default tier without prefix", "netpol", "default", false),
		Entry("GlobalNetworkPolicy in default tier with prefix", "default.netpol", "default", false),
		Entry("GlobalNetworkPolicy in custom tier with correct prefix", "tier1.netpol", "tier1", false),
		Entry("GlobalNetworkPolicy in custom tier without prefix", "netpol", "tier1", true),
		Entry("GlobalNetworkPolicy in custom tier with incorrect prefix", "tier1.netpol", "tier2", true),
	)

	Describe("GlobalNetworkPolicy watch functionality", func() {
		It("should handle watch events for different resource versions and event types", func() {
			By("Listing GlobalNetworkPolicies with the latest resource version and checking for two results with name1/spec2 and name2/spec2")
			outList, outError := c.GlobalNetworkPolicies().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(HaveLen(0))
			rev0 := outList.ResourceVersion

			By("Configuring a GlobalNetworkPolicy name1/spec1 and storing the response")
			outRes1, err := c.GlobalNetworkPolicies().Create(
				ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: name1},
					Spec:       spec1,
				},
				options.SetOptions{},
			)
			Expect(err).NotTo(HaveOccurred())

			rev1 := outRes1.ResourceVersion

			By("Configuring a GlobalNetworkPolicy name2/spec2 and storing the response")
			outRes2, err := c.GlobalNetworkPolicies().Create(
				ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: name2},
					Spec:       spec2,
				},
				options.SetOptions{},
			)

			By("Starting a watcher from revision rev1 - this should skip the first creation")
			w, err := c.GlobalNetworkPolicies().Watch(ctx, options.ListOptions{ResourceVersion: rev1})
			Expect(err).NotTo(HaveOccurred())
			testWatcher1 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher1.Stop()

			By("Deleting res1")
			_, err = c.GlobalNetworkPolicies().Delete(ctx, name1, options.DeleteOptions{})
			Expect(err).NotTo(HaveOccurred())

			By("Checking for two events, create res2 and delete re1")
			testWatcher1.ExpectEvents(apiv3.KindGlobalNetworkPolicy, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes2,
				},
				{
					Type:     watch.Deleted,
					Previous: outRes1,
				},
			})
			testWatcher1.Stop()

			By("Starting a watcher from rev0 - this should get all events")
			w, err = c.GlobalNetworkPolicies().Watch(ctx, options.ListOptions{ResourceVersion: rev0})
			Expect(err).NotTo(HaveOccurred())
			testWatcher2 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher2.Stop()

			By("Modifying res2")
			outRes3, err := c.GlobalNetworkPolicies().Update(
				ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: outRes2.ObjectMeta,
					Spec:       spec1,
				},
				options.SetOptions{},
			)
			Expect(err).NotTo(HaveOccurred())
			testWatcher2.ExpectEvents(apiv3.KindGlobalNetworkPolicy, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes1,
				},
				{
					Type:   watch.Added,
					Object: outRes2,
				},
				{
					Type:     watch.Deleted,
					Previous: outRes1,
				},
				{
					Type:     watch.Modified,
					Previous: outRes2,
					Object:   outRes3,
				},
			})
			testWatcher2.Stop()

			// Only etcdv3 supports watching a specific instance of a resource.
			if config.Spec.DatastoreType == apiconfig.EtcdV3 {
				By("Starting a watcher from rev0 watching name1 - this should get all events for name1")
				w, err = c.GlobalNetworkPolicies().Watch(ctx, options.ListOptions{Name: name1, ResourceVersion: rev0})
				Expect(err).NotTo(HaveOccurred())
				testWatcher2_1 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
				defer testWatcher2_1.Stop()
				testWatcher2_1.ExpectEvents(apiv3.KindGlobalNetworkPolicy, []watch.Event{
					{
						Type:   watch.Added,
						Object: outRes1,
					},
					{
						Type:     watch.Deleted,
						Previous: outRes1,
					},
				})
				testWatcher2_1.Stop()
			}

			By("Starting a watcher not specifying a rev - expect the current snapshot")
			w, err = c.GlobalNetworkPolicies().Watch(ctx, options.ListOptions{})
			Expect(err).NotTo(HaveOccurred())
			testWatcher3 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher3.Stop()
			testWatcher3.ExpectEvents(apiv3.KindGlobalNetworkPolicy, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes3,
				},
			})
			testWatcher3.Stop()

			By("Configuring GlobalNetworkPolicy name1/spec1 again and storing the response")
			outRes1, err = c.GlobalNetworkPolicies().Create(
				ctx,
				&apiv3.GlobalNetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: name1},
					Spec:       spec1,
				},
				options.SetOptions{},
			)

			By("Starting a watcher not specifying a rev - expect the current snapshot")
			w, err = c.GlobalNetworkPolicies().Watch(ctx, options.ListOptions{})
			Expect(err).NotTo(HaveOccurred())
			testWatcher4 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher4.Stop()
			testWatcher4.ExpectEventsAnyOrder(apiv3.KindGlobalNetworkPolicy, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes1,
				},
				{
					Type:   watch.Added,
					Object: outRes3,
				},
			})

			By("Cleaning the datastore and expecting deletion events for each configured resource (tests prefix deletes results in individual events for each key)")
			be.Clean()
			testWatcher4.ExpectEvents(apiv3.KindGlobalNetworkPolicy, []watch.Event{
				{
					Type:     watch.Deleted,
					Previous: outRes1,
				},
				{
					Type:     watch.Deleted,
					Previous: outRes3,
				},
			})
			testWatcher4.Stop()
		})
	})
})
