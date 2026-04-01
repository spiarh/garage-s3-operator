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

	garage "git.deuxfleurs.fr/garage-sdk/garage-admin-sdk-golang"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	garagev1alpha1 "github.com/spiarh/garage-s3-operator/api/v1alpha1"
)

func createBucketFixtures(ctx context.Context, garageURL string, name string, mutate func(*garagev1alpha1.GarageBucket)) *garagev1alpha1.GarageBucket {
	cluster := createGarageClusterFixtures(ctx, garageURL, name)

	bucket := &garagev1alpha1.GarageBucket{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: garagev1alpha1.BucketSpec{
			ClusterRef:      &garagev1alpha1.Selector{Name: cluster.Name, Namespace: cluster.Namespace},
			BucketAccessKey: &garagev1alpha1.BucketAccessKey{Enabled: false},
		},
	}
	if mutate != nil {
		mutate(bucket)
	}
	Expect(k8sClient.Create(ctx, bucket)).To(Succeed())
	DeferCleanup(func() {
		cleanupResourceWithFinalizer(ctx, bucket)
	})
	DeferCleanup(func() {
		accessKey := &garagev1alpha1.GarageAccessKey{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			return
		}
		cleanupResourceWithFinalizer(ctx, accessKey)
	})
	DeferCleanup(func() {
		perm := &garagev1alpha1.GaragePermission{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			return
		}
		cleanupResourceWithFinalizer(ctx, perm)
	})
	DeferCleanup(func() {
		resourceSecret := &corev1.Secret{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, resourceSecret)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			return
		}
		err = k8sClient.Delete(ctx, resourceSecret)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})

	return bucket
}

func hasOwnerReferenceToBucket(object metav1.Object, bucket *garagev1alpha1.GarageBucket) bool {
	for _, ref := range object.GetOwnerReferences() {
		if ref.APIVersion == garagev1alpha1.GroupVersion.String() &&
			ref.Kind == "GarageBucket" &&
			ref.Name == bucket.Name &&
			ref.UID == bucket.UID {
			return true
		}
	}

	return false
}

func deleteBucket(ctx context.Context, bucket *garagev1alpha1.GarageBucket) {
	fresh := &garagev1alpha1.GarageBucket{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, fresh)).To(Succeed())
	Expect(k8sClient.Delete(ctx, fresh)).To(Succeed())

	Eventually(func() bool {
		fresh := &garagev1alpha1.GarageBucket{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, fresh)
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())
}

func queueCreateKeyFlow(mock *GarageMockServer, bucket fakeGarageBucket, permissions *garagev1alpha1.Permissions, keyTargetName string) {
	createdKey := fakeGarageKey{
		ID:          "key-" + keyTargetName,
		Name:        keyTargetName,
		SecretKey:   "secret-" + keyTargetName,
		Bucket:      &bucket,
		Permissions: permissions,
	}

	mock.QueueResponder(http.MethodGet, "/v2/ListKeys", respondWithEmptyKeyList)
	mock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(createdKey))
	mock.QueueResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(&bucket, permissions))
	mock.SetFallbackResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(&bucket, permissions))
	mock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucket, permissions))
	mock.SetFallbackResponder(http.MethodPost, "/v2/AllowBucketKey", respondWithBucketPermissionUpdate(bucket))
	mock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", respondWithBucketPermissionUpdate(bucket))
	mock.SetFallbackResponder(http.MethodGet, "/v2/ListBuckets", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
		writeJSON(w, http.StatusOK, []garage.ListBucketsResponseItem{typedBucketListItem(bucket)})
	})
}

var _ = Describe("Bucket Controller", func() {
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

		It("creates a bucket and patches status on the happy path", func() {
			name := "creates-a-bucket-and-patches-status-on-the-happy-path"
			By("creating bucket fixtures")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, nil)
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for bucket creation and deletion")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))
			garageMock.QueueResponder(http.MethodPost, "/v2/DeleteBucket", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
				w.WriteHeader(http.StatusOK)
			})

			By("waiting for reconciliation to create the Garage bucket and patch status")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageBucket{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(createdBucket.ID))
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodGet, "/v2/GetBucketInfo")).To(BeNumerically(">=", 1))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateBucket")).To(Equal(1))
				g.Expect(garageMock.LastAuthHeader()).To(Equal("Bearer test-token"))
			}).Should(Succeed())

			By("deleting the Kubernetes bucket resource")
			deleteBucket(ctx, bucket)

			By("verifying the Garage bucket was deleted")
			Eventually(func(g Gomega) {
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DeleteBucket")).To(Equal(1))
				g.Expect(garageMock.RequestCount(http.MethodGet, "/v2/GetBucketInfo")).To(BeNumerically(">=", 2))
			}).Should(Succeed())
		})

		It("creates associated access key and permission resources when createKey is enabled", func() {
			name := "creates-associated-access-key-and-permission-resources-when-createkey-is-enabled"
			expectedPermissions := &garagev1alpha1.Permissions{
				Owner: false,
				Read:  true,
				Write: false,
			}
			By("creating bucket fixtures with automatic key creation enabled")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, func(bucket *garagev1alpha1.GarageBucket) {
				bucket.Spec.BucketAccessKey = &garagev1alpha1.BucketAccessKey{
					Enabled:     true,
					Permissions: expectedPermissions,
				}
			})
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for bucket and key provisioning")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))
			queueCreateKeyFlow(garageMock, createdBucket, expectedPermissions, name)

			By("waiting for reconciliation to create the bucket, key, and permission resources")
			Eventually(func(g Gomega) {
				freshBucket := &garagev1alpha1.GarageBucket{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, freshBucket)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(freshBucket.Status.ID).To(Equal(createdBucket.ID))
				g.Expect(garageMock.RequestCount(http.MethodGet, "/v2/GetBucketInfo")).To(BeNumerically(">=", 1))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateBucket")).To(Equal(1))

				accessKey := &garagev1alpha1.GarageAccessKey{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(accessKey.Spec.ClusterRef).NotTo(BeNil())
				g.Expect(*accessKey.Spec.ClusterRef).To(Equal(*freshBucket.Spec.ClusterRef))
				g.Expect(accessKey.Spec.Secret).NotTo(BeNil())
				g.Expect(accessKey.Spec.Secret.Format).To(Equal(garagev1alpha1.DefaultFormat))
				g.Expect(controllerutil.ContainsFinalizer(accessKey, finalizer)).To(BeTrue())
				g.Expect(hasOwnerReferenceToBucket(accessKey, freshBucket)).To(BeTrue())

				perm := &garagev1alpha1.GaragePermission{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(perm.Spec.KeyRef).NotTo(BeNil())
				g.Expect(*perm.Spec.KeyRef).To(Equal(*accessKey.Selector()))
				g.Expect(perm.Spec.BucketRef).NotTo(BeNil())
				g.Expect(*perm.Spec.BucketRef).To(Equal(*freshBucket.Selector()))
				g.Expect(perm.Spec.Permissions).NotTo(BeNil())
				g.Expect(*perm.Spec.Permissions).To(Equal(*expectedPermissions))
				g.Expect(controllerutil.ContainsFinalizer(perm, finalizer)).To(BeTrue())
				g.Expect(hasOwnerReferenceToBucket(perm, freshBucket)).To(BeTrue())
				g.Expect(garageMock.LastAuthHeader()).To(Equal("Bearer test-token"))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateKey")).To(BeNumerically(">=", 1))
			}).Should(Succeed())
		})

		It("creates associated access key resources with the configured secret format", func() {
			name := "creates-associated-access-key-resources-with-the-configured-secret-format"
			By("creating bucket fixtures with automatic key creation enabled and an explicit secret format")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, func(bucket *garagev1alpha1.GarageBucket) {
				bucket.Spec.BucketAccessKey = &garagev1alpha1.BucketAccessKey{
					Enabled: true,
					Format:  garagev1alpha1.AWSConfigFormat,
				}
			})
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for bucket and key provisioning")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))
			queueCreateKeyFlow(garageMock, createdBucket, &garagev1alpha1.Permissions{Owner: true, Read: true, Write: true}, name)

			By("waiting for reconciliation to propagate the configured format to the managed access key")
			Eventually(func(g Gomega) {
				accessKey := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(accessKey.Spec.Secret).NotTo(BeNil())
				g.Expect(accessKey.Spec.Secret.Format).To(Equal(garagev1alpha1.AWSConfigFormat))
			}).Should(Succeed())
		})

		It("deletes associated access resources when createKey is disabled", func() {
			name := "deletes-associated-access-resources-when-createkey-is-disabled"
			expectedPermissions := &garagev1alpha1.Permissions{
				Owner: false,
				Read:  true,
				Write: false,
			}
			By("creating bucket fixtures with automatic key creation enabled")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, func(bucket *garagev1alpha1.GarageBucket) {
				bucket.Spec.BucketAccessKey = &garagev1alpha1.BucketAccessKey{
					Enabled:     true,
					Permissions: expectedPermissions,
				}
			})
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for bucket, key, permission, and deletion flows")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))
			queueCreateKeyFlow(garageMock, createdBucket, expectedPermissions, name)
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DeleteKey", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
				w.WriteHeader(http.StatusOK)
			})

			By("waiting for reconciliation to create the managed access resources")
			Eventually(func(g Gomega) {
				accessKey := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
				g.Expect(err).NotTo(HaveOccurred())

				perm := &garagev1alpha1.GaragePermission{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("disabling automatic key creation on the bucket")
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, freshBucket)).To(Succeed())
			freshBucket.Spec.BucketAccessKey.Enabled = false
			Expect(k8sClient.Update(ctx, freshBucket)).To(Succeed())

			By("waiting for the managed access resources to be deleted")
			Eventually(func(g Gomega) {
				accessKey := &garagev1alpha1.GarageAccessKey{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

				perm := &garagev1alpha1.GaragePermission{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DeleteKey")).To(BeNumerically(">=", 1))
			}).Should(Succeed())
		})

		It("does not create associated key resources when createKey is omitted", func() {
			name := "does-not-create-associated-key-resources-when-createkey-is-omitted"
			By("creating bucket fixtures without a createKey section")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, func(bucket *garagev1alpha1.GarageBucket) {
				bucket.Spec.BucketAccessKey = nil
			})
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for bucket creation only")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))

			By("waiting for reconciliation to create the bucket without child resources")
			Eventually(func(g Gomega) {
				freshBucket := &garagev1alpha1.GarageBucket{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, freshBucket)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(freshBucket.Status.ID).To(Equal(createdBucket.ID))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateBucket")).To(Equal(1))

				accessKey := &garagev1alpha1.GarageAccessKey{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

				perm := &garagev1alpha1.GaragePermission{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateKey")).To(Equal(0))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/AllowBucketKey")).To(Equal(0))
			}).Should(Succeed())
		})

		It("does not create access key resources when createKey is omitted", func() {
			name := "does-not-create-access-key-resources-when-createkey-is-omitted"
			By("creating bucket fixtures without a createKey configuration")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, func(bucket *garagev1alpha1.GarageBucket) {
				bucket.Spec.BucketAccessKey = nil
			})
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for bucket creation")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))

			By("waiting for reconciliation to create only the Garage bucket")
			Eventually(func(g Gomega) {
				freshBucket := &garagev1alpha1.GarageBucket{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, freshBucket)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(freshBucket.Status.ID).To(Equal(createdBucket.ID))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateBucket")).To(Equal(1))

				accessKey := &garagev1alpha1.GarageAccessKey{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

				perm := &garagev1alpha1.GaragePermission{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateKey")).To(Equal(0))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/AllowBucketKey")).To(Equal(0))
			}).Should(Succeed())
		})

		It("deletes the target bucket in Garage and keeps owner-reference cleanup signals when createKey is enabled", func() {
			name := "deletes-the-target-bucket-in-garage-and-keeps-owner-reference-cleanup-signals-when-createkey-is-enabled"
			expectedPermissions := &garagev1alpha1.Permissions{
				Owner: false,
				Read:  true,
				Write: false,
			}
			By("creating bucket fixtures with managed key resources")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, func(bucket *garagev1alpha1.GarageBucket) {
				bucket.Spec.BucketAccessKey = &garagev1alpha1.BucketAccessKey{
					Enabled:     true,
					Permissions: expectedPermissions,
				}
			})
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for create and delete flows")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))
			queueCreateKeyFlow(garageMock, createdBucket, expectedPermissions, name)
			garageMock.QueueResponder(http.MethodPost, "/v2/DeleteBucket", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
				w.WriteHeader(http.StatusOK)
			})

			By("waiting for owned key resources to be created with owner references")
			Eventually(func(g Gomega) {
				freshBucket := &garagev1alpha1.GarageBucket{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, freshBucket)
				g.Expect(err).NotTo(HaveOccurred())

				accessKey := &garagev1alpha1.GarageAccessKey{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hasOwnerReferenceToBucket(accessKey, freshBucket)).To(BeTrue())

				perm := &garagev1alpha1.GaragePermission{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hasOwnerReferenceToBucket(perm, freshBucket)).To(BeTrue())
			}).Should(Succeed())

			By("confirming owner references remain in place before deletion")
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, freshBucket)).To(Succeed())

			accessKey := &garagev1alpha1.GarageAccessKey{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, accessKey)).To(Succeed())
			perm := &garagev1alpha1.GaragePermission{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, perm)).To(Succeed())
			Expect(hasOwnerReferenceToBucket(accessKey, freshBucket)).To(BeTrue())
			Expect(hasOwnerReferenceToBucket(perm, freshBucket)).To(BeTrue())

			By("deleting the Kubernetes bucket resource")
			deleteBucket(ctx, bucket)

			By("verifying Garage bucket deletion was requested")
			Eventually(func(g Gomega) {
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DeleteBucket")).To(Equal(1))
			}).Should(Succeed())
		})

		It("does not delete the target bucket when reclaimPolicy is Retain", func() {
			name := "does-not-delete-the-target-bucket-when-reclaimpolicy-is-retain"
			By("creating bucket fixtures with a retain reclaim policy")
			bucket := createBucketFixtures(ctx, garageMock.URL(), name, func(bucket *garagev1alpha1.GarageBucket) {
				bucket.Spec.ReclaimPolicy = garagev1alpha1.Retain
			})
			createdBucket := fakeGarageBucket{ID: "bucket-" + name, Alias: name}

			By("configuring Garage API responses for bucket lookup and creation")
			garageMock.QueueResponder(http.MethodGet, "/v2/GetBucketInfo", respondNotFound)
			garageMock.QueueResponder(http.MethodPost, "/v2/CreateBucket", respondWithBucket(createdBucket))
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(createdBucket))

			By("waiting for reconciliation to create the Garage bucket")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GarageBucket{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bucket.Name, Namespace: bucket.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.Status.ID).To(Equal(createdBucket.ID))
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/CreateBucket")).To(Equal(1))
			}).Should(Succeed())

			By("deleting the Kubernetes bucket resource")
			deleteBucket(ctx, bucket)

			By("verifying Garage bucket deletion is skipped")
			Consistently(func(g Gomega) {
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DeleteBucket")).To(Equal(0))
			}, "500ms", "100ms").Should(Succeed())
		})
	})
})
