/*
Copyright 2025 spiarh.

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

package controller

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	garagev1alpha1 "github.com/spiarh/garage-s3-operator/api/v1alpha1"
)

func createKeyFixtures(ctx context.Context, garageURL string, name string, mutate func(*garagev1alpha1.GarageAccessKey)) *garagev1alpha1.GarageAccessKey {
	cluster := createGarageClusterFixtures(ctx, garageURL, name)

	key := &garagev1alpha1.GarageAccessKey{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: garagev1alpha1.GarageAccessKeySpec{
			ClusterRef: &garagev1alpha1.Selector{Name: cluster.Name, Namespace: cluster.Namespace},
		},
	}
	if mutate != nil {
		mutate(key)
	}
	Expect(k8sClient.Create(ctx, key)).To(Succeed())
	DeferCleanup(func() {
		cleanupResourceWithFinalizer(ctx, key)
	})
	DeferCleanup(func() {
		resourceSecret := &corev1.Secret{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, resourceSecret)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			return
		}
		err = k8sClient.Delete(ctx, resourceSecret)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})

	return key
}

func deleteKeyResource(ctx context.Context, key *garagev1alpha1.GarageAccessKey) {
	fresh := &garagev1alpha1.GarageAccessKey{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)).To(Succeed())
	Expect(k8sClient.Delete(ctx, fresh)).To(Succeed())

	Eventually(func() bool {
		fresh := &garagev1alpha1.GarageAccessKey{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())
}

func keyTargetName(ctx context.Context, key *garagev1alpha1.GarageAccessKey) string {
	fresh := &garagev1alpha1.GarageAccessKey{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)).To(Succeed())
	return fresh.TargetName()
}

var _ = Describe("Key Controller", func() {
	Context("when reconciling against the Garage API", func() {
		var (
			ctx        context.Context
			garageMock *GarageMockServer
		)

		BeforeEach(func() {
			ctx = context.Background()
			garageMock = NewGarageMockServer()
			DeferCleanup(garageMock.Close)
		})

		It("creates a Garage key, patches status, and creates the secret", func() {
			name := "creates-a-garage-key-patches-status-and-creates-the-secret"
			By("creating access key fixtures")
			key := createKeyFixtures(ctx, garageMock.URL(), name, nil)
			targetName := keyTargetName(ctx, key)
			createdKey := fakeGarageKey{
				ID:        "key-" + targetName,
				Name:      targetName,
				SecretKey: "secret-" + targetName,
			}

			By("configuring Garage API responses for key creation")
			garageMock.QueueResponder(http.MethodGet, "/v2/ListKeys", respondWithEmptyKeyList)
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(createdKey))
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(nil, nil))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(nil, nil))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(nil, nil))

			By("waiting for reconciliation to create the Garage key and secret")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(createdKey.ID))
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateKey")).To(BeNumerically(">=", 1))
				g.Expect(garageMock.LastAuthHeader()).To(Equal("Bearer test-token"))

				secret := &corev1.Secret{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, secret)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(secret.Data["AWS_ACCESS_KEY_ID"]).To(Equal([]byte(createdKey.ID)))
				g.Expect(secret.Data["AWS_SECRET_ACCESS_KEY"]).To(Equal([]byte(createdKey.SecretKey)))
			}).Should(Succeed())
		})

		It("creates the secret in AWS config format when requested", func() {
			name := "creates-the-secret-in-aws-config-format-when-requested"
			By("creating access key fixtures with AWS config secret format")
			key := createKeyFixtures(ctx, garageMock.URL(), name, func(key *garagev1alpha1.GarageAccessKey) {
				key.Spec.Secret = &garagev1alpha1.AccessKeySecret{
					Format: garagev1alpha1.AWSConfigFormat,
				}
			})
			targetName := keyTargetName(ctx, key)
			createdKey := fakeGarageKey{
				ID:        "key-" + targetName,
				Name:      targetName,
				SecretKey: "secret-" + targetName,
			}

			By("configuring Garage API responses for key creation")
			garageMock.QueueResponder(http.MethodGet, "/v2/ListKeys", respondWithEmptyKeyList)
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(createdKey))
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(nil, nil))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(nil, nil))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(nil, nil))

			By("waiting for reconciliation to create the Garage key and AWS config secret")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(createdKey.ID))

				secret := &corev1.Secret{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, secret)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(secret.Data).To(HaveKey("config"))
				g.Expect(secret.Data).NotTo(HaveKey("AWS_ACCESS_KEY_ID"))
				g.Expect(secret.Data).NotTo(HaveKey("AWS_SECRET_ACCESS_KEY"))
				g.Expect(string(secret.Data["config"])).To(ContainSubstring("aws_access_key_id=" + createdKey.ID))
				g.Expect(string(secret.Data["config"])).To(ContainSubstring("aws_secret_access_key=" + createdKey.SecretKey))
			}).Should(Succeed())
		})

		It("reuses an existing Garage key without creating a new one", func() {
			name := "reuses-an-existing-garage-key-without-creating-a-new-one"
			By("creating access key fixtures")
			key := createKeyFixtures(ctx, garageMock.URL(), name, nil)
			targetName := keyTargetName(ctx, key)
			existingKey := fakeGarageKey{
				ID:        "key-" + targetName,
				Name:      targetName,
				SecretKey: "secret-" + targetName,
			}

			By("configuring Garage API responses for an existing key")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(existingKey))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(nil, nil))

			By("waiting for reconciliation to reuse the existing key and publish the secret")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(existingKey.ID))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateKey")).To(Equal(0))

				secret := &corev1.Secret{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, secret)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(secret.Data["AWS_ACCESS_KEY_ID"]).To(Equal([]byte(existingKey.ID)))
				g.Expect(secret.Data["AWS_SECRET_ACCESS_KEY"]).To(Equal([]byte(existingKey.SecretKey)))
			}).Should(Succeed())
		})

		It("deletes the Garage key when reclaimPolicy is Delete", func() {
			name := "deletes-the-garage-key-when-reclaimpolicy-is-delete"
			By("creating access key fixtures")
			key := createKeyFixtures(ctx, garageMock.URL(), name, nil)
			targetName := keyTargetName(ctx, key)
			createdKey := fakeGarageKey{
				ID:        "key-" + targetName,
				Name:      targetName,
				SecretKey: "secret-" + targetName,
			}

			By("configuring Garage API responses for key creation and deletion")
			garageMock.QueueResponder(http.MethodGet, "/v2/ListKeys", respondWithEmptyKeyList)
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(createdKey))
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(nil, nil))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(nil, nil))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(nil, nil))
			garageMock.QueueResponder(http.MethodPost, "/v2/DeleteKey", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
				w.WriteHeader(http.StatusOK)
			})

			By("waiting for reconciliation to persist the Garage key id")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(createdKey.ID))
			}).Should(Succeed())

			By("deleting the Kubernetes access key resource")
			deleteKeyResource(ctx, key)

			By("verifying Garage key deletion was requested")
			Eventually(func(g Gomega) {
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DeleteKey")).To(Equal(1))
			}).Should(Succeed())
		})

		It("does not delete the Garage key when reclaimPolicy is Retain", func() {
			name := "does-not-delete-the-garage-key-when-reclaimpolicy-is-retain"
			By("creating access key fixtures with a retain reclaim policy")
			key := createKeyFixtures(ctx, garageMock.URL(), name, func(key *garagev1alpha1.GarageAccessKey) {
				key.Spec.ReclaimPolicy = garagev1alpha1.Retain
			})
			targetName := keyTargetName(ctx, key)
			existingKey := fakeGarageKey{
				ID:        "key-" + targetName,
				Name:      targetName,
				SecretKey: "secret-" + targetName,
			}

			By("configuring Garage API responses for an existing retained key")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(existingKey))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(nil, nil))

			By("waiting for reconciliation to observe the retained key")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(existingKey.ID))
			}).Should(Succeed())

			By("deleting the Kubernetes access key resource")
			deleteKeyResource(ctx, key)

			By("verifying Garage key deletion is skipped")
			Consistently(func(g Gomega) {
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DeleteKey")).To(Equal(0))
			}, "500ms", "100ms").Should(Succeed())
		})

		It("treats a 400 response as target key not found during deletion", func() {
			name := "treats-a-400-response-as-target-key-not-found-during-deletion"
			By("creating access key fixtures")
			key := createKeyFixtures(ctx, garageMock.URL(), name, nil)
			targetName := keyTargetName(ctx, key)
			existingKey := fakeGarageKey{
				ID:        "key-" + targetName,
				Name:      targetName,
				SecretKey: "secret-" + targetName,
			}

			By("configuring Garage API responses so the key disappears before deletion")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(existingKey))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", func(w http.ResponseWriter, r *http.Request, _ *GarageMockServer) {
				//nolint:goconst
				if r.URL.Query().Get("showSecretKey") == "true" {
					writeJSON(w, http.StatusOK, typedKeyResponse(existingKey))
					return
				}
				http.Error(w, "missing key", http.StatusBadRequest)
			})

			By("waiting for reconciliation to observe the key before deletion")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(existingKey.ID))
			}).Should(Succeed())

			By("deleting the Kubernetes access key resource")
			deleteKeyResource(ctx, key)

			By("verifying Garage key deletion is skipped when the key is already gone")
			Consistently(func(g Gomega) {
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DeleteKey")).To(Equal(0))
			}, "500ms", "100ms").Should(Succeed())
		})
	})
})
