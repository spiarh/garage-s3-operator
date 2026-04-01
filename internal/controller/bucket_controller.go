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
	"cmp"
	"context"
	"fmt"
	"net/http"

	"github.com/go-logr/logr"
	apierror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/spiarh/garage-s3-operator/api/v1alpha1"
)

const finalizer = "s3.spiarh.fr/finalizer"

// BucketReconciler reconciles a Bucket object
type BucketReconciler struct {
	Log                  logr.Logger
	Scheme               *runtime.Scheme
	GarageManager        GarageAPI
	GarageManagerFactory GarageManagerFactory

	client.Client
}

// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garageclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garageclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garageclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garagebuckets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garagebuckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garagebuckets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log = logf.FromContext(ctx)

	r.Log.Info("starting reconciliation")

	bucket := &v1alpha1.GarageBucket{}
	err := r.Get(ctx, req.NamespacedName, bucket)
	if err != nil {
		if apierror.IsNotFound(err) {
			r.Log.V(3).Info("GarageBucket not found, skipping reconciliation")
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	cluster, err := getCluster(ctx, r.Client, bucket.Spec.ClusterRef)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to fetch Cluster: %w", err)
	}

	factory := r.GarageManagerFactory
	if factory == nil {
		factory = defaultGarageManagerFactory
	}

	r.GarageManager, err = factory(ctx, r.Client, r.Log, *cluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to create garage manager: %w", err)
	}

	if !bucket.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(bucket, finalizer) {
			if err := r.reconcileDeletion(ctx, bucket); err != nil {
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(bucket, finalizer)
			if err := r.Update(ctx, bucket); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(bucket, finalizer) {
		controllerutil.AddFinalizer(bucket, finalizer)
		if err := r.Update(ctx, bucket); err != nil {
			return ctrl.Result{}, err
		}
	}

	err = r.reconcile(ctx, bucket)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.Log.Info("reconciliation done")

	return ctrl.Result{}, nil
}

func (r *BucketReconciler) reconcile(ctx context.Context, bucket *v1alpha1.GarageBucket) error {
	bucketName := bucket.TargetName()
	bucketInfo, resp, err := r.GarageManager.getBucket(bucketName)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			r.Log.Info("target bucket not found, creating it")
		} else {
			return fmt.Errorf("unable to get target bucket info: %w", err)
		}
	}

	if resp.StatusCode == http.StatusNotFound {
		bucketInfo, resp, err = r.GarageManager.createBucket(bucketName)
		if err != nil {
			if resp != nil {
				r.Log.Error(err, "unable to create target bucket: "+resp.Status)
			}
			r.Log.Error(err, "unable to create target bucket")
			return err
		}
	}

	if bucket.Status.ID == "" {
		r.Log.Info("patch GarageBucket status")

		patch := client.MergeFrom(bucket.DeepCopy())
		bucket.Status.ID = bucketInfo.Id
		err := r.Status().Patch(ctx, bucket, patch)
		if err != nil {
			return fmt.Errorf("unable to patch GarageBucket status: %w", err)
		}

		r.Log.Info("GarageBucket status patched successfully")
	}

	if bucket.Spec.Quotas != nil {
		if bucket.Spec.Quotas.MaxObjects != bucketInfo.Quotas.GetMaxObjects() ||
			bucket.Spec.Quotas.MaxSize != bucketInfo.Quotas.GetMaxSize() {
			_, _, err = r.GarageManager.updateBucket(bucketInfo.Id, bucket.Spec.Quotas)
			if err != nil {
				return fmt.Errorf("unable to set quotas: %w", err)
			}
		}
	}

	if bucket.Spec.BucketAccessKey == nil || !bucket.Spec.BucketAccessKey.Enabled {
		r.Log.V(3).Info("accesskey creation disabled, deleting managed key resources")
		if err := r.reconcileKeyResourcesDeletion(ctx, bucket); err != nil {
			return fmt.Errorf("unable to delete managed key resources: %w", err)
		}
		return nil
	}

	r.Log.V(3).Info("key creation enabled, reconciling GarageAccessKey")
	key, err := r.reconcileAccessKey(ctx, bucket)
	if err != nil {
		return fmt.Errorf("GarageAccessKey reconciliation failed: %w", err)
	}

	r.Log.V(3).Info("key creation enabled, reconciling GaragePermission")
	if err := r.reconcilePermission(ctx, bucket, key); err != nil {
		return fmt.Errorf("GaragePermission reconciliation failed: %w", err)
	}

	return nil
}

func (r *BucketReconciler) reconcileKeyResourcesDeletion(ctx context.Context, bucket *v1alpha1.GarageBucket) error {
	key := &v1alpha1.GarageAccessKey{}
	keyName := client.ObjectKey{Name: bucket.Name, Namespace: bucket.Namespace}
	if err := r.Get(ctx, keyName, key); err != nil {
		if !apierror.IsNotFound(err) {
			return fmt.Errorf("unable to fetch managed GarageAccessKey: %w", err)
		}
	} else {
		hasOwnerRef, err := controllerutil.HasOwnerReference(key.GetOwnerReferences(), bucket, r.Scheme)
		if err != nil {
			return fmt.Errorf("unable to inspect GarageAccessKey owner references: %w", err)
		}
		if hasOwnerRef {
			r.Log.Info("deleting managed GarageAccessKey")
			if err := r.Delete(ctx, key); err != nil && !apierror.IsNotFound(err) {
				return fmt.Errorf("unable to delete managed GarageAccessKey: %w", err)
			}
		}
	}

	perm := &v1alpha1.GaragePermission{}
	permName := client.ObjectKey{Name: bucket.Name, Namespace: bucket.Namespace}
	if err := r.Get(ctx, permName, perm); err != nil {
		if !apierror.IsNotFound(err) {
			return fmt.Errorf("unable to fetch managed GaragePermission: %w", err)
		}
	} else {
		hasOwnerRef, err := controllerutil.HasOwnerReference(perm.GetOwnerReferences(), bucket, r.Scheme)
		if err != nil {
			return fmt.Errorf("unable to inspect GaragePermission owner references: %w", err)
		}
		if hasOwnerRef {
			r.Log.Info("deleting managed GaragePermission")
			if err := r.Delete(ctx, perm); err != nil && !apierror.IsNotFound(err) {
				return fmt.Errorf("unable to delete managed GaragePermission: %w", err)
			}
		}
	}

	return nil
}

func (r *BucketReconciler) reconcileDeletion(_ context.Context, bucket *v1alpha1.GarageBucket) error {
	if bucket.Spec.ReclaimPolicy == v1alpha1.Retain {
		r.Log.Info("reclaim policy set to Retain, skipping target bucket deletion")
		return nil
	}

	bucketName := bucket.TargetName()
	bucketInfo, resp, err := r.GarageManager.getBucket(bucketName)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			r.Log.Info("target bucket not found, deletion not required")
			return nil
		}
		return fmt.Errorf("unable to get target bucket info: %w", err)
	}

	resp, err = r.GarageManager.deleteBucket(bucketInfo.Id)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("unable to delete target bucket %s: %w", resp.Status, err)
		}
		return fmt.Errorf("unable to delete target bucket: %w", err)
	}

	r.Log.Info("target bucket deleted")

	return nil
}

func (r *BucketReconciler) reconcileAccessKey(ctx context.Context, bucket *v1alpha1.GarageBucket) (*v1alpha1.GarageAccessKey, error) {
	r.Log.Info("ensure GarageKey exists for bucket")

	key := &v1alpha1.GarageAccessKey{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bucket.Name,
			Namespace: bucket.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, key, func() error {
		if bucket.Namespace == key.Namespace {
			hasOwnerRef, err := controllerutil.HasOwnerReference(
				key.GetOwnerReferences(), bucket, r.Scheme)
			if err != nil {
				return err
			}

			if key.Spec.ClusterRef != nil && !hasOwnerRef {
				return fmt.Errorf("key already exists and is not owned/managed by the bucket")
			}

			if err = controllerutil.SetOwnerReference(bucket, key, r.Scheme); err != nil {
				return err
			}
		}

		if !controllerutil.ContainsFinalizer(key, finalizer) {
			controllerutil.AddFinalizer(key, finalizer)
		}

		key.Spec = v1alpha1.GarageAccessKeySpec{
			ClusterRef: bucket.Spec.ClusterRef,
			Secret: &v1alpha1.AccessKeySecret{
				Format: cmp.Or(bucket.Spec.BucketAccessKey.Format, v1alpha1.DefaultFormat),
			},
		}

		return nil
	})
	if err != nil {
		r.Log.Error(err, "unable to create or update GarageKey")
		return nil, err
	}

	switch op {
	case controllerutil.OperationResultCreated:
		r.Log.Info("GarageKey created")
	case controllerutil.OperationResultUpdated:
		r.Log.Info("GarageKey updated")
	case controllerutil.OperationResultNone:
		r.Log.V(3).Info("GarageKey was up-to-date, nothing done")
	}

	return key, nil
}

func (r *BucketReconciler) reconcilePermission(ctx context.Context, bucket *v1alpha1.GarageBucket, key *v1alpha1.GarageAccessKey) error {
	r.Log.Info("ensure GaragePermission exists for key")

	perm := &v1alpha1.GaragePermission{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bucket.Name,
			Namespace: bucket.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, perm, func() error {
		if bucket.Namespace == perm.Namespace {
			hasOwnerRef, err := controllerutil.HasOwnerReference(
				perm.GetOwnerReferences(), bucket, r.Scheme)
			if err != nil {
				return err
			}

			if perm.Spec.BucketRef != nil && !hasOwnerRef {
				return fmt.Errorf("key already exists and is not owned/managed by the bucket")
			}
			err = controllerutil.SetOwnerReference(bucket, perm, r.Scheme)
			if err != nil {
				return err
			}
		}

		if !controllerutil.ContainsFinalizer(perm, finalizer) {
			controllerutil.AddFinalizer(perm, finalizer)
		}

		perm.Spec = v1alpha1.GaragePermissionSpec{}
		perm.Spec.KeyRef = key.Selector()
		perm.Spec.BucketRef = bucket.Selector()
		perm.Spec.Permissions = bucket.Spec.BucketAccessKey.Permissions

		if perm.Spec.Permissions == nil {
			perm.Spec.Permissions = &v1alpha1.Permissions{
				Owner: true,
				Read:  true,
				Write: true,
			}
		}

		return nil
	})
	if err != nil {
		r.Log.Error(err, "unable to create or update Permission")
		return err
	}

	switch op {
	case controllerutil.OperationResultCreated:
		r.Log.Info("Permission created")
	case controllerutil.OperationResultUpdated:
		r.Log.Info("Permission updated")
	case controllerutil.OperationResultNone:
		r.Log.V(3).Info("Permission was up-to-date, nothing done")
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.GarageBucket{}).
		Named("bucket").
		Complete(r)
}
