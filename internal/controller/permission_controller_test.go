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

type permissionFixtures struct {
	bucket *garagev1alpha1.GarageBucket
	key    *garagev1alpha1.GarageAccessKey
	perm   *garagev1alpha1.GaragePermission
}

func createPermissionBaseFixtures(ctx context.Context, garageMock *GarageMockServer, name string) permissionFixtures {
	cluster := createGarageClusterFixtures(ctx, garageMock.URL(), name)

	bucket := &garagev1alpha1.GarageBucket{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-bucket", Namespace: "default"},
		Spec: garagev1alpha1.BucketSpec{
			ClusterRef:      &garagev1alpha1.Selector{Name: cluster.Name, Namespace: cluster.Namespace},
			BucketAccessKey: &garagev1alpha1.BucketAccessKey{Enabled: false},
		},
		Status: garagev1alpha1.GarageBucketStatus{ID: "bucket-" + name},
	}
	bucketInfo := fakeGarageBucket{ID: bucket.Status.ID, Alias: bucket.TargetName()}
	garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetBucketInfo", respondWithBucket(bucketInfo))
	Expect(k8sClient.Create(ctx, bucket)).To(Succeed())
	DeferCleanup(func() {
		cleanupResourceWithFinalizer(ctx, bucket)
	})

	key := &garagev1alpha1.GarageAccessKey{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-key", Namespace: "default"},
		Spec: garagev1alpha1.GarageAccessKeySpec{
			ClusterRef: &garagev1alpha1.Selector{Name: cluster.Name, Namespace: cluster.Namespace},
		},
		Status: garagev1alpha1.GarageAccessKeyStatus{ID: "key-" + name},
	}
	garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithEmptyKeyList)
	garageMock.SetFallbackResponder(http.MethodPost, "/v2/CreateKey", respondWithCreatedKeyFromBody(nil, nil))
	garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(nil, nil))
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

	Eventually(func(g Gomega) {
		fresh := &garagev1alpha1.GarageAccessKey{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, fresh)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(fresh.Status.ID).NotTo(BeEmpty())
	}).Should(Succeed())

	freshKey := &garagev1alpha1.GarageAccessKey{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, freshKey)).To(Succeed())
	createdKey := fakeGarageKey{
		ID:        freshKey.Status.ID,
		Name:      freshKey.TargetName(),
		SecretKey: "secret-" + freshKey.TargetName(),
		Bucket:    &bucketInfo,
	}
	garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListKeys", respondWithKeyList(createdKey))
	garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucketInfo, nil))
	garageMock.SetFallbackResponder(http.MethodGet, "/v2/ListBuckets", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
		writeJSON(w, http.StatusOK, []garage.ListBucketsResponseItem{typedBucketListItem(bucketInfo)})
	})

	return permissionFixtures{
		bucket: bucket,
		key:    key,
		perm: &garagev1alpha1.GaragePermission{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		},
	}
}

func createPermissionResource(ctx context.Context, fixtures permissionFixtures, permissions *garagev1alpha1.Permissions) {
	freshBucket := &garagev1alpha1.GarageBucket{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
	freshKey := &garagev1alpha1.GarageAccessKey{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.key.Name, Namespace: fixtures.key.Namespace}, freshKey)).To(Succeed())

	perm := &garagev1alpha1.GaragePermission{
		ObjectMeta: metav1.ObjectMeta{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace},
		Spec: garagev1alpha1.GaragePermissionSpec{
			KeyRef:      freshKey.Selector(),
			BucketRef:   freshBucket.Selector(),
			Permissions: permissions,
		},
	}
	Expect(k8sClient.Create(ctx, perm)).To(Succeed())
	DeferCleanup(func() {
		cleanupResourceWithFinalizer(ctx, perm)
	})
}

func deletePermissionResource(ctx context.Context, perm *garagev1alpha1.GaragePermission) {
	fresh := &garagev1alpha1.GaragePermission{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: perm.Name, Namespace: perm.Namespace}, fresh)).To(Succeed())
	Expect(k8sClient.Delete(ctx, fresh)).To(Succeed())

	Eventually(func() bool {
		fresh := &garagev1alpha1.GaragePermission{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: perm.Name, Namespace: perm.Namespace}, fresh)
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())
}

var _ = Describe("Permission Controller", func() {
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

		It("grants missing permissions on the Garage bucket", func() {
			name := "grants-missing-permissions-on-the-garage-bucket"
			expectedPermissions := &garagev1alpha1.Permissions{Owner: true, Read: true, Write: true}
			By("creating bucket and key fixtures for the permission resource")
			fixtures := createPermissionBaseFixtures(ctx, garageMock, name)
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
			bucketInfo := fakeGarageBucket{ID: freshBucket.Status.ID, Alias: freshBucket.TargetName()}

			By("configuring Garage API responses for missing permissions")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucketInfo, &garagev1alpha1.Permissions{}))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/AllowBucketKey", respondWithBucketPermissionUpdate(bucketInfo))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", respondWithBucketPermissionUpdate(bucketInfo))

			By("creating the permission resource")
			createPermissionResource(ctx, fixtures, expectedPermissions)

			By("waiting for reconciliation to grant the requested permissions")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/AllowBucketKey")).To(BeNumerically(">=", 1))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DenyBucketKey")).To(BeNumerically(">=", 1))
				g.Expect(garageMock.LastAuthHeader()).To(Equal("Bearer test-token"))
			}).Should(Succeed())
		})

		It("skips permission updates when the Garage key already has the expected permissions", func() {
			name := "skips-permission-updates-when-the-garage-key-already-has-the-expected-permissions"
			expectedPermissions := &garagev1alpha1.Permissions{Owner: true, Read: true, Write: true}
			By("creating bucket and key fixtures for the permission resource")
			fixtures := createPermissionBaseFixtures(ctx, garageMock, name)
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
			bucketInfo := fakeGarageBucket{ID: freshBucket.Status.ID, Alias: freshBucket.TargetName()}

			By("configuring Garage API responses for matching permissions")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucketInfo, expectedPermissions))

			By("creating the permission resource")
			createPermissionResource(ctx, fixtures, expectedPermissions)

			By("waiting for reconciliation to skip permission updates")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/AllowBucketKey")).To(Equal(0))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DenyBucketKey")).To(Equal(0))
			}).Should(Succeed())
		})

		It("updates permissions when only some permission bits already match", func() {
			name := "updates-permissions-when-only-some-permission-bits-already-match"
			expectedPermissions := &garagev1alpha1.Permissions{Owner: true, Read: true, Write: true}
			partialPermissions := &garagev1alpha1.Permissions{Owner: true, Read: false, Write: false}
			By("creating bucket and key fixtures for the permission resource")
			fixtures := createPermissionBaseFixtures(ctx, garageMock, name)
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
			bucketInfo := fakeGarageBucket{ID: freshBucket.Status.ID, Alias: freshBucket.TargetName()}

			By("configuring Garage API responses for partially matching permissions")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucketInfo, partialPermissions))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/AllowBucketKey", respondWithBucketPermissionUpdate(bucketInfo))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", respondWithBucketPermissionUpdate(bucketInfo))

			By("creating the permission resource")
			createPermissionResource(ctx, fixtures, expectedPermissions)

			By("waiting for reconciliation to update the remaining permission bits")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/AllowBucketKey")).To(BeNumerically(">=", 1))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DenyBucketKey")).To(BeNumerically(">=", 1))
			}).Should(Succeed())
		})

		It("updates permissions when Garage returns bucket permissions without explicit fields", func() {
			name := "updates-permissions-when-garage-returns-bucket-permissions-without-explicit-fields"
			expectedPermissions := &garagev1alpha1.Permissions{Owner: true, Read: false, Write: false}
			By("creating bucket and key fixtures for the permission resource")
			fixtures := createPermissionBaseFixtures(ctx, garageMock, name)
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
			bucketInfo := fakeGarageBucket{ID: freshBucket.Status.ID, Alias: freshBucket.TargetName()}

			By("configuring Garage API responses with empty permission fields")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", func(w http.ResponseWriter, r *http.Request, _ *GarageMockServer) {
				search := r.URL.Query().Get("search")
				Expect(search).NotTo(BeEmpty())

				response := typedKeyResponse(fakeGarageKey{
					ID:            "key-" + search,
					Name:          search,
					SecretKey:     "secret-" + search,
					Bucket:        &bucketInfo,
					Permissions:   nil,
					ShowSecretKey: r.URL.Query().Get("showSecretKey") == "true",
				})
				response.Buckets[0].Permissions = garage.ApiBucketKeyPerm{}
				writeJSON(w, http.StatusOK, response)
			})
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/AllowBucketKey", respondWithBucketPermissionUpdate(bucketInfo))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", respondWithBucketPermissionUpdate(bucketInfo))

			By("creating the permission resource")
			createPermissionResource(ctx, fixtures, expectedPermissions)

			By("waiting for reconciliation to treat unset permission fields as missing")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/AllowBucketKey")).To(BeNumerically(">=", 1))
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DenyBucketKey")).To(BeNumerically(">=", 1))
			}).Should(Succeed())
		})

		It("revokes permissions when the GaragePermission is deleted", func() {
			name := "revokes-permissions-when-the-garagepermission-is-deleted"
			expectedPermissions := &garagev1alpha1.Permissions{Owner: true, Read: true, Write: true}
			By("creating bucket and key fixtures for the permission resource")
			fixtures := createPermissionBaseFixtures(ctx, garageMock, name)
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
			bucketInfo := fakeGarageBucket{ID: freshBucket.Status.ID, Alias: freshBucket.TargetName()}

			By("configuring Garage API responses for permission revocation")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucketInfo, expectedPermissions))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", respondWithBucketPermissionUpdate(bucketInfo))

			By("creating the permission resource")
			createPermissionResource(ctx, fixtures, expectedPermissions)

			By("waiting for reconciliation to add the finalizer")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
			}).Should(Succeed())

			By("deleting the permission resource")
			deletePermissionResource(ctx, fixtures.perm)

			By("verifying Garage permissions were revoked")
			Eventually(func(g Gomega) {
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DenyBucketKey")).To(BeNumerically(">=", 1))
			}).Should(Succeed())
		})

		It("keeps retrying deletion when permission revocation fails", func() {
			name := "keeps-retrying-deletion-when-permission-revocation-fails"
			expectedPermissions := &garagev1alpha1.Permissions{Owner: true, Read: true, Write: true}
			By("creating bucket and key fixtures for the permission resource")
			fixtures := createPermissionBaseFixtures(ctx, garageMock, name)
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
			bucketInfo := fakeGarageBucket{ID: freshBucket.Status.ID, Alias: freshBucket.TargetName()}

			By("configuring Garage API responses so revocation fails")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucketInfo, expectedPermissions))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
				http.Error(w, "revocation failed", http.StatusInternalServerError)
			})

			By("creating the permission resource")
			createPermissionResource(ctx, fixtures, expectedPermissions)

			By("waiting for reconciliation to add the finalizer")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
			}).Should(Succeed())

			By("deleting the permission resource")
			freshPerm := &garagev1alpha1.GaragePermission{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, freshPerm)).To(Succeed())
			Expect(k8sClient.Delete(ctx, freshPerm)).To(Succeed())

			By("verifying the resource stays pending deletion because revocation failed")
			Consistently(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fresh.DeletionTimestamp.IsZero()).To(BeFalse())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
				g.Expect(garageMock.RequestCount(http.MethodPost, "/v2/DenyBucketKey")).To(BeNumerically(">=", 1))
			}, "700ms", "100ms").Should(Succeed())

			By("cleaning up the stuck resource after the assertion")
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", respondWithBucketPermissionUpdate(bucketInfo))
			Eventually(func() bool {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("removes the finalizer when the target bucket no longer exists", func() {
			name := "removes-the-finalizer-when-the-target-bucket-no-longer-exists"
			By("creating bucket and key fixtures for the permission resource")
			fixtures := createPermissionBaseFixtures(ctx, garageMock, name)
			freshBucket := &garagev1alpha1.GarageBucket{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.bucket.Name, Namespace: fixtures.bucket.Namespace}, freshBucket)).To(Succeed())
			bucketInfo := fakeGarageBucket{ID: freshBucket.Status.ID, Alias: freshBucket.TargetName()}

			By("configuring Garage API responses for the initial permission grant")
			garageMock.SetFallbackResponder(http.MethodGet, "/v2/GetKeyInfo", respondWithKeyInfoForBucket(&bucketInfo, &garagev1alpha1.Permissions{}))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/AllowBucketKey", respondWithBucketPermissionUpdate(bucketInfo))
			garageMock.SetFallbackResponder(http.MethodPost, "/v2/DenyBucketKey", respondWithBucketPermissionUpdate(bucketInfo))
			garageMock.QueueResponder(http.MethodPost, "/v2/DeleteBucket", func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
				w.WriteHeader(http.StatusOK)
			})

			By("creating the permission resource")
			createPermissionResource(ctx, fixtures, &garagev1alpha1.Permissions{Owner: true, Read: false, Write: false})

			By("waiting for reconciliation to add the finalizer")
			Eventually(func(g Gomega) {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(controllerutil.ContainsFinalizer(fresh, finalizer)).To(BeTrue())
			}).Should(Succeed())

			By("deleting the bucket and permission resources")
			Expect(k8sClient.Delete(ctx, freshBucket)).To(Succeed())
			freshPerm := &garagev1alpha1.GaragePermission{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, freshPerm)).To(Succeed())
			Expect(k8sClient.Delete(ctx, freshPerm)).To(Succeed())

			By("verifying the permission resource is fully removed")
			Eventually(func() bool {
				fresh := &garagev1alpha1.GaragePermission{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtures.perm.Name, Namespace: fixtures.perm.Namespace}, fresh)
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})
})
